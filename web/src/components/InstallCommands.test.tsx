import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { InstallCommands } from "./InstallCommands";

afterEach(() => localStorage.clear());

const script = "https://github.com/xjetry/heron-probe/releases/download/v1.2.3/install.sh";
const curl = () => screen.getByLabelText("curl 安装命令").textContent;
const wget = () => screen.getByLabelText("wget 安装命令").textContent;
const sshArgs = () => screen.queryByLabelText("SSH 反代参数")?.textContent;
const domestic = () => screen.getByRole("checkbox", { name: /国内主机/ });
const portInput = () => screen.getByRole("textbox", { name: "本机代理端口" });
const renderCommands = () => render(<InstallCommands hubVersion="v1.2.3" boundAgentVersion="v1.2.3" origin="https://hub.example:28080" registerKey="k1" />);

describe("InstallCommands 国内主机", () => {
  it("默认关闭：命令不带 --update-source，没有 SSH 反代参数", () => {
    renderCommands();
    expect(domestic()).not.toBeChecked();
    expect(curl()).toBe(`curl -fsSL ${script} | sh -s -- --hub https://hub.example:28080 --key k1`);
    expect(wget()).toBe(`wget -qO- ${script} | sh -s -- --hub https://hub.example:28080 --key k1`);
    expect(sshArgs()).toBeUndefined();
  });

  it("打开后两条安装命令都带 --update-source hub，并给出默认端口 7897 的 SSH 反代参数", () => {
    renderCommands();
    fireEvent.click(domestic());
    expect(curl()).toBe(`curl -fsSL ${script} | sh -s -- --hub https://hub.example:28080 --key k1 --update-source hub`);
    expect(wget()).toBe(`wget -qO- ${script} | sh -s -- --hub https://hub.example:28080 --key k1 --update-source hub`);
    expect(sshArgs()).toBe("-t -R 127.0.0.1:7897:127.0.0.1:7897 'export http_proxy=http://127.0.0.1:7897; export https_proxy=http://127.0.0.1:7897; export all_proxy=socks5h://127.0.0.1:7897; exec $SHELL -l'");
  });

  it("改端口后 SSH 参数里的端口一起改，端口记在浏览器里，开关每次默认关闭", () => {
    const { unmount } = renderCommands();
    fireEvent.click(domestic());
    fireEvent.change(portInput(), { target: { value: "7890" } });
    expect(sshArgs()).toBe("-t -R 127.0.0.1:7890:127.0.0.1:7890 'export http_proxy=http://127.0.0.1:7890; export https_proxy=http://127.0.0.1:7890; export all_proxy=socks5h://127.0.0.1:7890; exec $SHELL -l'");
    unmount();
    renderCommands();
    expect(domestic()).not.toBeChecked();
    fireEvent.click(domestic());
    expect(portInput()).toHaveValue("7890");
    expect(sshArgs()).toContain("127.0.0.1:7890:127.0.0.1:7890");
  });

  // 端口拼进可复制的 shell 命令：不合法的输入一律不出命令，也不覆盖已记住的端口。
  it.each(["0", "65536", "07890", "78a", "", "7890; rm -rf /", " 7890"])("端口 %j 不合法时不给 SSH 参数", (value) => {
    localStorage.setItem("heron-install-proxy-port", "7890");
    renderCommands();
    fireEvent.click(domestic());
    fireEvent.change(portInput(), { target: { value } });
    expect(sshArgs()).toBeUndefined();
    expect(screen.getByRole("alert")).toHaveTextContent("1–65535");
    expect(localStorage.getItem("heron-install-proxy-port")).toBe("7890");
    expect(curl()).toContain("--update-source hub");
  });

  it("记住的端口不合法时回到默认 7897", () => {
    localStorage.setItem("heron-install-proxy-port", "not-a-port");
    renderCommands();
    fireEvent.click(domestic());
    expect(portInput()).toHaveValue("7897");
  });
});

describe("InstallCommands Hub 连接地址", () => {
  const hubInput = () => screen.getByRole("textbox", { name: "Hub 连接地址" });
  const editHub = (value: string) => fireEvent.change(hubInput(), { target: { value } });
  const commandsFor = (hub: string, rest = "--key k1") => [
    `curl -fsSL ${script} | sh -s -- --hub ${hub} ${rest}`,
    `wget -qO- ${script} | sh -s -- --hub ${hub} ${rest}`,
  ];

  it("默认留空：占位是当前域名，命令用传入的 origin", () => {
    renderCommands();
    expect(hubInput()).toHaveValue("");
    expect(hubInput()).toHaveAttribute("placeholder", "https://hub.example:28080");
    expect([curl(), wget()]).toEqual(commandsFor("https://hub.example:28080"));
  });

  it("填了覆盖地址后两条命令的 --hub 都用它的规范写法，并立即记在浏览器里", () => {
    renderCommands();
    editHub("HTTPS://Heron-Panel.O1.pw:28080/");
    expect([curl(), wget()]).toEqual(commandsFor("https://heron-panel.o1.pw:28080"));
    expect(localStorage.getItem("heron-install-hub")).toBe("https://heron-panel.o1.pw:28080");
  });

  it("重新打开沿用记住的覆盖地址", () => {
    const { unmount } = renderCommands();
    editHub("https://heron-panel.o1.pw:28080");
    unmount();
    renderCommands();
    expect(hubInput()).toHaveValue("https://heron-panel.o1.pw:28080");
    expect([curl(), wget()]).toEqual(commandsFor("https://heron-panel.o1.pw:28080"));
  });

  it("清空即删除记住的地址，命令回到当前域名", () => {
    localStorage.setItem("heron-install-hub", "https://heron-panel.o1.pw:28080");
    renderCommands();
    expect([curl(), wget()]).toEqual(commandsFor("https://heron-panel.o1.pw:28080"));
    editHub("  ");
    expect(localStorage.getItem("heron-install-hub")).toBeNull();
    expect([curl(), wget()]).toEqual(commandsFor("https://hub.example:28080"));
  });

  // 覆盖地址未加引号地进 shell 命令：不合法时一律不出命令（含 SSH 反代参数），也不覆盖已记住的地址。
  it.each(["https://hub.example/admin", "hub.example:28080", "ftp://hub.example", "https://a$(reboot).example", "https://user@hub.example"])("覆盖地址 %j 不合法时不出任何命令", (value) => {
    localStorage.setItem("heron-install-hub", "https://heron-panel.o1.pw:28080");
    renderCommands();
    fireEvent.click(domestic());
    editHub(value);
    expect(screen.getByRole("alert")).toHaveTextContent("Hub 连接地址须为");
    expect(screen.queryByLabelText("curl 安装命令")).toBeNull();
    expect(screen.queryByLabelText("wget 安装命令")).toBeNull();
    expect(sshArgs()).toBeUndefined();
    expect(document.body.textContent).not.toContain("sh -s --");
    expect(localStorage.getItem("heron-install-hub")).toBe("https://heron-panel.o1.pw:28080");
  });

  it("记住的值不合法时按空处理", () => {
    localStorage.setItem("heron-install-hub", "https://hub.example/admin");
    renderCommands();
    expect(hubInput()).toHaveValue("");
    expect([curl(), wget()]).toEqual(commandsFor("https://hub.example:28080"));
  });

  // --insecure-http 与明文提示跟 --hub 读同一个生效地址，两个方向都钉住。
  it("http 覆盖地址带 --insecure-http 与明文提示，即使当前域名是 https", () => {
    renderCommands();
    editHub("http://heron-panel.o1.pw:28080");
    expect([curl(), wget()]).toEqual(commandsFor("http://heron-panel.o1.pw:28080", "--key k1 --insecure-http"));
    expect(screen.getByText(/token 与指标将明文传输/)).toBeInTheDocument();
  });

  it("https 覆盖地址不带 --insecure-http，即使当前域名是 http", () => {
    render(<InstallCommands hubVersion="v1.2.3" boundAgentVersion="v1.2.3" origin="http://hub.example:8080" registerKey="k1" />);
    expect(curl()).toContain("--insecure-http");
    editHub("https://heron-panel.o1.pw:28080");
    expect([curl(), wget()]).toEqual(commandsFor("https://heron-panel.o1.pw:28080"));
    expect(screen.queryByText(/token 与指标将明文传输/)).toBeNull();
  });

  it("与国内主机开关同时生效，换 token 的重新注册命令也用覆盖地址", () => {
    render(<InstallCommands hubVersion="v1.2.3" boundAgentVersion="v1.2.3" origin="https://hub.example:28080" registerKey="k1" reRegister />);
    editHub("https://heron-panel.o1.pw:28080");
    fireEvent.click(domestic());
    expect([curl(), wget()]).toEqual(commandsFor("https://heron-panel.o1.pw:28080", "--key k1 --re-register --update-source hub"));
    expect(sshArgs()).toContain("127.0.0.1:7897:127.0.0.1:7897");
  });
});
