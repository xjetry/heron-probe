import { readFileSync } from "node:fs";
import { join } from "node:path";
import { screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { Layout } from "./components/Layout";
import { PublicLayout } from "./public/Layout";
import { PublicService } from "./gen/probe/v1/public_pb";
import { renderWithAdmin, renderWithService } from "./test/harness";

// styles.css 是管理面板与公开页共用的唯一全局样式表：两边的 header 都套 .nav 取同一份
// flex 布局。面板专用的 nowrap 规则必须锁在 .panel-nav 上——放在共享的 .nav 上会连带
// 命中公开页头部的站点标题链接，标题变长时不再折行，整页横向溢出。
function nowrapSelector(): string {
  const css = readFileSync(join(import.meta.dirname, "styles.css"), "utf8");
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);
  for (const rule of Array.from(style.sheet!.cssRules)) {
    if (rule instanceof CSSStyleRule && rule.style.getPropertyValue("white-space") === "nowrap") {
      return rule.selectorText;
    }
  }
  throw new Error("styles.css: 没有找到 white-space: nowrap 的规则");
}

it("面板导航的 nowrap 规则命中面板链接、不命中公开页头部链接", async () => {
  const selector = nowrapSelector();

  renderWithService(PublicService, { getSite: async () => ({}) }, [{ path: "/", Component: PublicLayout }], "/");
  const brand = await screen.findByRole("link");
  expect(brand.matches(selector)).toBe(false);

  renderWithAdmin({}, [{ path: "/", Component: Layout, children: [{ index: true, element: <p /> }] }], "/");
  // 正控制：同一选择器确实命中面板导航链接，排除“选择器写错、两边都不命中”这种误判为通过的可能。
  const overview = screen.getByRole("link", { name: "总览" });
  expect(overview.matches(selector)).toBe(true);
});
