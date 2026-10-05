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
