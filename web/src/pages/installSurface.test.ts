// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const installSh = readFileSync(resolve(root, "deploy/install.sh"), "utf8");
const panel = readFileSync(resolve(root, "web/src/components/InstallCommands.tsx"), "utf8");

describe("面板安装命令与 install.sh", () => {
  it("命令里的每个 --xxx 都是 install.sh 接受的选项", () => {
    const accepted = new Set([...installSh.matchAll(/^\s*(--[A-Za-z0-9-]+)\)/gm)].map((m) => m[1]));
    const used = [...new Set([...panel.matchAll(/--[A-Za-z0-9-]+/g)].map((m) => m[0]))];
    expect(used.length).toBeGreaterThan(0);
    for (const flag of used) {
      expect(accepted, `${flag} 不在 install.sh 的 case 标签里`).toContain(flag);
    }
  });

  // 脚本只装自己所属的版本：版本由面板取哪个 URL 的脚本决定，命令里没有第二个版本来源。
  it("install.sh 不接受 --version，面板命令也不带", () => {
    const accepted = new Set([...installSh.matchAll(/^\s*(--[A-Za-z0-9-]+)\)/gm)].map((m) => m[1]));
    expect(accepted).toContain("--insecure-http");
    expect(accepted).not.toContain("--version");
    expect(panel).not.toMatch(/--version\b/);
  });

  it("install.sh 的 REPO 与面板里的仓库地址一致", () => {
    const repo = installSh.match(/^REPO=(\S+)/m)?.[1];
    expect(repo).toBeTruthy();
    const urls = [...panel.matchAll(/https:\/\/github\.com\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+/g)].map((m) => m[0]);
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) {
      expect(url === repo || url.startsWith(`${repo}/`)).toBe(true);
    }
  });
});
