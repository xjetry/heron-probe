import { test as base, expect, request } from "@playwright/test";

export const ADMIN_PASSWORD = "local-browser-test-password";

export class HubError extends Error {
  constructor(readonly method: string, readonly status: number, readonly body: string) {
    super(`${method}: ${status} ${body}`);
  }
}

export type Hub = {
  // 以独立会话调用 AdminService；非 2xx 抛 HubError。
  rpc: (method: string, body?: unknown) => Promise<any>;
  // 登记用例结束时要做的事：不论用例成败、是否超时，都在 fixture 拆除时按登记的逆序执行。
  atEnd: (label: string, fn: () => Promise<unknown>) => void;
  // 用例结束时调用一个删除过程（DeleteNode、DeleteProbeTask、DeleteApiToken 等）；用例自己已经删掉的（404）不算泄漏。
  deleteAtEnd: (method: string, body: unknown) => void;
  deleteNodeAtEnd: (id: string) => void;
};

// e2e 的所有 spec 共用一个 hub 与一个库，一个用例留下的节点、主题或总闸状态会让后面无关的用例连带失败。收尾因此不放在
// 用例自己的 finally 里：用例超时时 Playwright 先关掉页面，finally 里借页面发的请求全部失败。hub fixture 用自己登录的
// 请求上下文（不带 Origin，管理接口的同源检查放行），收尾在 fixture 拆除阶段执行，有自己的时限，不依赖用例的页面还活着；
// 任何一项收尾失败都让拆除报错，留下的状态在报告里点名，而不是悄悄污染后面的用例。
export const test = base.extend<{ hub: Hub }>({
  hub: [async ({ baseURL }, use) => {
    const context = await request.newContext({ baseURL, ignoreHTTPSErrors: true });
    const rpc = async (method: string, body: unknown = {}) => {
      const response = await context.post(`/heron.v1.AdminService/${method}`, { data: body });
      const text = await response.text();
      if (!response.ok()) throw new HubError(method, response.status(), text);
      return text ? JSON.parse(text) : {};
    };
    await rpc("Login", { password: ADMIN_PASSWORD });
    const tasks: { label: string; fn: () => Promise<unknown> }[] = [];
    const atEnd = (label: string, fn: () => Promise<unknown>) => { tasks.push({ label, fn }); };
    const deleteAtEnd = (method: string, body: unknown) => atEnd(`${method} ${JSON.stringify(body)}`, async () => {
      try {
        await rpc(method, body);
      } catch (error) {
        if (!(error instanceof HubError && error.status === 404)) throw error;
      }
    });
    await use({ rpc, atEnd, deleteAtEnd, deleteNodeAtEnd: (id) => deleteAtEnd("DeleteNode", { id }) });
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
