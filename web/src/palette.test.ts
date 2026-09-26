// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const css = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

// 明暗只经 color-scheme 切换：公开页把站点设置写在 html 的 data-theme 上，它要能压过系统设置。
// 按系统明暗的媒体查询改写变量会绕过 data-theme，所以一处都不能有。浏览器里的实际效果由公开页的浏览器验收核对。
it("调色板随 color-scheme 取值，data-theme 能强制明暗", () => {
  expect(css).not.toMatch(/prefers-color-scheme/);
  const root = css.match(/:root\s*\{([^}]*)\}/)[1];
  expect(root).toMatch(/color-scheme:\s*light dark/);
  for (const name of ["--bg", "--card", "--fg", "--muted", "--line", "--accent"]) {
    expect(root, name).toMatch(new RegExp(`${name}:\\s*light-dark\\(`));
  }
  expect(css).toMatch(/:root\[data-theme="light"\]\s*\{\s*color-scheme:\s*light;?\s*\}/);
  expect(css).toMatch(/:root\[data-theme="dark"\]\s*\{\s*color-scheme:\s*dark;?\s*\}/);
});
