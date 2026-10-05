import { afterEach, describe, expect, it, vi } from "vitest";
import { buildUserscript } from "../components/UserscriptButton";
import { sshProxyArgs } from "../lib/installProxy";

// 在 jsdom 里真实运行油猴脚本：GM 接口与 hub 应答用桩，走一遍"添加节点 → 结果面板"，在结果面板上操作。
type Request = { url: string; data: string; onload: (res: { status: number; responseText: string }) => void };

function hub(request: Request) {
  const method = request.url.split("/").pop();
  const body = JSON.parse(request.data);
  const reply = method === "GetSnapshot" ? { hubVersion: "v1.2.3" }
    : body.preview ? { expectedVersion: "7" }
    : { result: { token: "node-tok", node: { id: "5", name: "vps" } } };
  setTimeout(() => request.onload({ status: 200, responseText: JSON.stringify(reply) }), 0);
}

async function addNode(store: Map<string, string>) {
  vi.stubGlobal("GM_getValue", (key: string, fallback: string) => store.get(key) ?? fallback);
  vi.stubGlobal("GM_setValue", (key: string, value: string) => { store.set(key, value); });
  vi.stubGlobal("GM_registerMenuCommand", () => {});
  vi.stubGlobal("GM_xmlhttpRequest", hub);
  document.title = "vps";
  new Function(buildUserscript("https://hub.example:28080", "api-token"))();
  const root = (document.documentElement.lastElementChild as HTMLElement).shadowRoot!;
  await new Promise((resolve) => setTimeout(resolve, 0));
  (root.querySelector(".fab") as HTMLButtonElement).click();
  await vi.waitFor(() => expect(root.querySelector(".panel .primary")).not.toBeNull());
  (root.querySelector(".panel .primary") as HTMLButtonElement).click();
  await vi.waitFor(() => expect(root.querySelector('[aria-label="安装命令"]')).not.toBeNull());
  return root;
}

const field = (root: ShadowRoot, label: string) => root.querySelector(`[aria-label="${label}"]`) as HTMLInputElement | null;
const setPort = (root: ShadowRoot, value: string) => {
  const input = field(root, "本机代理端口")!;
  input.value = value;
  input.dispatchEvent(new Event("input"));
};
const script = "https://github.com/xjetry/heron-probe/releases/download/v1.2.3/install.sh";

afterEach(() => {
  document.documentElement.querySelectorAll(":scope > div").forEach((el) => el.remove());
  vi.unstubAllGlobals();
});

describe("油猴脚本 国内主机", () => {
  it("默认关闭；打开后安装命令带 --update-source hub，SSH 反代参数与面板同一拼法，复制拿到的是新命令", async () => {
    const root = await addNode(new Map());
    const command = `curl -fsSL ${script} | sh -s -- --hub https://hub.example:28080 --key node-tok`;
    expect(field(root, "国内主机")!.checked).toBe(false);
    expect(field(root, "安装命令")!.value).toBe(command);
    expect(field(root, "SSH 反代参数")).toBeNull();

    field(root, "国内主机")!.click();
    expect(field(root, "安装命令")!.value).toBe(`${command} --update-source hub`);
    expect(root.querySelector("pre")!.textContent).toBe(`${command} --update-source hub`);
    expect(field(root, "SSH 反代参数")!.value).toBe(sshProxyArgs(7897));

    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    (field(root, "安装命令")!.nextElementSibling as HTMLButtonElement).click();
    expect(writeText).toHaveBeenCalledWith(`${command} --update-source hub`);
  });

  it("改端口后 SSH 参数跟着改并记住端口；不合法的端口不出参数、不覆盖记住的值", async () => {
    const store = new Map<string, string>();
    const root = await addNode(store);
    field(root, "国内主机")!.click();
    setPort(root, "7890");
    expect(field(root, "SSH 反代参数")!.value).toBe(sshProxyArgs(7890));
    expect(store.get("proxy_port")).toBe("7890");
    for (const value of ["0", "65536", "07890", "78a", "", "7890; rm -rf /"]) {
      setPort(root, value);
      expect(field(root, "SSH 反代参数"), value).toBeNull();
      expect(root.querySelector(".proxy-error")!.textContent).toContain("1–65535");
      expect(store.get("proxy_port")).toBe("7890");
    }
  });

  it("下次添加沿用记住的端口", async () => {
    const root = await addNode(new Map([["proxy_port", "7890"]]));
    field(root, "国内主机")!.click();
    expect(field(root, "本机代理端口")!.value).toBe("7890");
    expect(field(root, "SSH 反代参数")!.value).toBe(sshProxyArgs(7890));
  });
});
