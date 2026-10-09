// @vitest-environment node
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";

const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
const root = /:root\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
const token = (name: string): string | undefined => new RegExp(`${name}:\\s*([^;]+);`).exec(root)?.[1].trim();

// 运行时 token 由 styles.css 定义；这张表独立核对设计规范，避免色值漂移。
it.each([
  ["--bg", "light-dark(#f6f7f9, #0b0d12)"],
  ["--card", "light-dark(#ffffff, #12151c)"],
  ["--line", "light-dark(#e4e7ec, #232836)"],
  ["--fg", "light-dark(#111827, #e6e8ee)"],
  ["--accent", "light-dark(#2563eb, #5b9bf8)"],
  ["--status-online", "#10b981"],
  ["--status-attention", "#f59e0b"],
  ["--status-offline", "#f43f5e"],
  ["--status-never", "#6b7280"],
  ["--status-maintenance", "#a78bfa"],
  ["--ok", "var(--status-online)"],
  ["--bad", "var(--status-offline)"],
  ["--warn", "var(--status-attention)"],
])("%s = %s", (name, value) => {
  expect(token(name)).toBe(value);
});

// vite.config.ts 用同一个正则读内置浅色主色；测试约束构建所依赖的声明格式。
it("--accent 保持 vite.config 能读出的写法", () => {
  expect(/--accent:\s*light-dark\(\s*(#[0-9a-fA-F]{6})\s*,/.exec(css)?.[1]).toBe("#2563eb");
});

// site.ts 可替换主色；状态色不引用主色，避免站点设置改变状态含义。
it("五个状态色都是字面量", () => {
  for (const name of ["online", "attention", "offline", "never", "maintenance"]) expect(token(`--status-${name}`)).toMatch(/^#[0-9a-f]{6}$/);
});

it("字体栈以内嵌的 Inter / JetBrains Mono 开头，样式表本身不声明或引用远程字体", () => {
  expect(token("--font-ui")).toMatch(/^"Inter Variable",/);
  expect(token("--font-mono")).toMatch(/^"JetBrains Mono Variable",/);
  expect(css).not.toMatch(/@font-face|@import|fonts\.googleapis/);
  expect(css).toMatch(/\.num\s*\{[^}]*font-family:\s*var\(--font-mono\)[^}]*font-variant-numeric:\s*tabular-nums/);
});
