import { create, createRegistry, fromJsonString, toJsonString, type DescMessage, type DescMethodUnary, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf";
import { test as base, expect, request, type Page, type Request, type Route } from "@playwright/test";
import { AdminService, file_heron_v1_admin } from "../src/gen/heron/v1/admin_pb";
import { file_heron_v1_agent } from "../src/gen/heron/v1/agent_pb";
import { file_heron_v1_public } from "../src/gen/heron/v1/public_pb";

export const ADMIN_PASSWORD = "local-browser-test-password";

// 请求与应答都经 @bufbuild/protobuf 的官方 JSON codec 编解码：int64、枚举、oneof、FieldMask、bytes、Any 都按
// proto3 JSON 映射处理，用例里不手写线上的 JSON 形状。Any（ExecuteChange 的 result）按类型 URL 找消息，注册表要
// 带上三个服务文件（它们各自引入 types、query 等依赖文件）。
const registry = createRegistry(file_heron_v1_admin, file_heron_v1_agent, file_heron_v1_public);

// Connect 协议的错误体（https://connectrpc.com/docs/protocol#error-end-stream）没有 proto 描述：按协议定义的形状核对后
// 读出 code 与 message；不是这个形状（例如反代或静态文件服务回的纯文本）时为 undefined，原文留在 text 里。
export type ConnectErrorBody = { code: string; message: string };

export type RpcResult<O extends DescMessage> =
  | { ok: true; message: MessageShape<O> }
  | { ok: false; method: string; status: number; error: ConnectErrorBody | undefined; text: string };

export class RpcError extends Error {
  constructor(readonly method: string, readonly status: number, readonly error: ConnectErrorBody | undefined, text: string) {
    super(`${method}: ${status} ${text}`);
  }
}

export const rpcPath = (method: DescMethodUnary): string => `/${method.parent.typeName}/${method.name}`;

function connectError(text: string): ConnectErrorBody | undefined {
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof body !== "object" || body === null || !("code" in body) || typeof body.code !== "string") return undefined;
  return { code: body.code, message: "message" in body && typeof body.message === "string" ? body.message : "" };
}

// 一次 unary 调用的线路部分：发出编码好的 JSON 字符串，拿回状态码与原文。编解码只在 Node 侧做。
type Send = (path: string, body: string, token: string | undefined) => Promise<{ status: number; text: string }>;

async function invoke<I extends DescMessage, O extends DescMessage>(send: Send, method: DescMethodUnary<I, O>, init: MessageInitShape<I>, token?: string): Promise<RpcResult<O>> {
  const body = toJsonString(method.input, create(method.input, init), { registry });
  const { status, text } = await send(rpcPath(method), body, token);
  if (status === 200) return { ok: true, message: fromJsonString(method.output, text, { registry }) };
  return { ok: false, method: method.name, status, error: connectError(text), text };
}

// 经页面调用：请求从浏览器发出，带着页面的 cookie 会话（管理端登录态、同源检查看到的 Origin）。给了 token 时按
// agent 或 API token 的方式鉴权，不带 cookie，免得 cookie 会话替它通过鉴权。page.evaluate 只收发字符串与状态码。
export function rpc<I extends DescMessage, O extends DescMessage>(page: Page, method: DescMethodUnary<I, O>, init: MessageInitShape<I>, token?: string): Promise<RpcResult<O>> {
  return invoke((path, body, token) => page.evaluate(async ({ path, body, token }) => {
    const response = await fetch(path, {
      method: "POST",
      credentials: token ? "omit" : "same-origin",
      headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body,
    });
    return { status: response.status, text: await response.text() };
  }, { path, body, token }), method, init, token);
}

// 取成功应答；失败时抛出带状态码与原文的错误，用例在调用点失败而不是拿着错误体往下走。
export function must<O extends DescMessage>(result: RpcResult<O>): MessageShape<O> {
  if (result.ok) return result.message;
  throw new RpcError(result.method, result.status, result.error, result.text);
}

// 以页面的 cookie 会话登录管理端。
export async function login(page: Page): Promise<void> {
  must(await rpc(page, AdminService.method.login, { password: ADMIN_PASSWORD }));
}

// 页面发出的请求消息：Connect 的 GET 把 JSON 放在 message 查询参数里，POST 放在正文里。
export function requestMessage<I extends DescMessage>(request: Request, method: { name: string; input: I }): MessageShape<I> {
  const json = request.method() === "GET" ? new URL(request.url()).searchParams.get("message") : request.postData();
  if (json === null) throw new Error(`${method.name}: request carries no message`);
  return fromJsonString(method.input, json, { registry });
}

// 拦截某个方法的路由模式与应答：按页面发来的请求消息给出应答消息，编码成 Connect 的 JSON 应答。
export const rpcRoute = (method: DescMethodUnary): string => `**${rpcPath(method)}**`;
export function fulfillRpc<I extends DescMessage, O extends DescMessage>(route: Route, method: DescMethodUnary<I, O>, respond: (request: MessageShape<I>) => MessageInitShape<O>): Promise<void> {
  const message = create(method.output, respond(requestMessage(route.request(), method)));
  return route.fulfill({ contentType: "application/json", body: toJsonString(method.output, message, { registry }) });
}

export type Hub = {
  // 以独立会话调用 AdminService，返回可判别的结果；要成功应答时用 must 取出。
  rpc: <I extends DescMessage, O extends DescMessage>(method: DescMethodUnary<I, O>, init: MessageInitShape<I>) => Promise<RpcResult<O>>;
  // 登记用例结束时要做的事：不论用例成败、是否超时，都在 fixture 拆除时按登记的逆序执行。
  atEnd: (label: string, fn: () => Promise<unknown>) => void;
  // 用例结束时调用一个删除过程（DeleteNode、DeleteProbeTask、DeleteApiToken 等）；用例自己已经删掉的（404）不算泄漏。
  deleteAtEnd: <I extends DescMessage>(method: DescMethodUnary<I, DescMessage>, init: MessageInitShape<I>) => void;
  deleteNodeAtEnd: (id: bigint) => void;
};

// e2e 的所有 spec 共用一个 hub 与一个库，一个用例留下的节点、主题或总闸状态会让后面无关的用例连带失败。收尾因此不放在
// 用例自己的 finally 里：用例超时时 Playwright 先关掉页面，finally 里借页面发的请求全部失败。hub fixture 用自己登录的
// 请求上下文（不带 Origin，管理接口的同源检查放行），收尾在 fixture 拆除阶段执行，有自己的时限，不依赖用例的页面还活着；
// 任何一项收尾失败都让拆除报错，留下的状态在报告里点名，而不是悄悄污染后面的用例。
export const test = base.extend<{ hub: Hub }>({
  hub: [async ({ baseURL }, use) => {
    const context = await request.newContext({ baseURL, ignoreHTTPSErrors: true });
    const send: Send = async (path, body) => {
      const response = await context.post(path, { data: body, headers: { "Content-Type": "application/json" } });
      return { status: response.status(), text: await response.text() };
    };
    const hubRpc: Hub["rpc"] = (method, init) => invoke(send, method, init);
    must(await hubRpc(AdminService.method.login, { password: ADMIN_PASSWORD }));
    const tasks: { label: string; fn: () => Promise<unknown> }[] = [];
    const atEnd = (label: string, fn: () => Promise<unknown>) => { tasks.push({ label, fn }); };
    const deleteAtEnd: Hub["deleteAtEnd"] = (method, init) => atEnd(`${method.name} ${toJsonString(method.input, create(method.input, init))}`, async () => {
      const result = await hubRpc(method, init);
      if (!result.ok && result.status !== 404) throw new RpcError(result.method, result.status, result.error, result.text);
    });
    await use({ rpc: hubRpc, atEnd, deleteAtEnd, deleteNodeAtEnd: (id) => deleteAtEnd(AdminService.method.deleteNode, { id }) });
    const failures: string[] = [];
    for (const task of tasks.reverse()) {
      try {
        await task.fn();
      } catch (error) {
        failures.push(`${task.label}: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
    await context.dispose();
    if (failures.length > 0) throw new Error(`用例收尾失败，状态留在了 hub 里：\n${failures.join("\n")}`);
  }, { timeout: 30_000 }],
});

export { expect };
