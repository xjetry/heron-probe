import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it } from "vitest";
import { PublicSiteSchema } from "../gen/probe/v1/public_pb";
import { applySite, DEFAULT_TITLE } from "./site";

afterEach(() => {
  document.head.innerHTML = "";
  document.documentElement.removeAttribute("style");
  delete document.documentElement.dataset.theme;
  delete document.body.dataset.pwned;
});

it("明暗、主色与标题写到文档上，撤销后恢复；auto 跟随系统", () => {
  const root = document.documentElement;
  const undo = applySite(create(PublicSiteSchema, { title: "机房", theme: "dark", accentColor: "#123abc" }));
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.getPropertyValue("--accent")).toBe("#123abc");
  expect(document.title).toBe("机房");
  undo();
  expect(root.dataset.theme).toBeUndefined();
  expect(root.style.getPropertyValue("--accent")).toBe("");
  applySite(create(PublicSiteSchema, { theme: "auto" }));
  expect(root.dataset.theme).toBeUndefined();
  expect(document.title).toBe(DEFAULT_TITLE);
});

it("自定义 CSS 排在已有样式之后，撤销时移除", () => {
  const builtIn = document.createElement("link");
  builtIn.rel = "stylesheet";
  document.head.append(builtIn);
  const undo = applySite(create(PublicSiteSchema, { theme: "auto", customCss: "body { color: red }" }));
  const last = document.head.lastElementChild as HTMLStyleElement;
  expect(last.tagName).toBe("STYLE");
  expect(last.dataset.siteCss).toBe("");
  expect(last.textContent).toBe("body { color: red }");
  undo();
  expect(document.head.querySelector("style[data-site-css]")).toBeNull();
});

// hub 拒绝含 "</" 的 CSS；即便有这样的值到达，textContent 也不经 HTML 解析，产生不了任何元素。
it("CSS 按文本写入，不产生元素", () => {
  const css = `</style><img src=x onerror="document.body.dataset.pwned='1'">`;
  applySite(create(PublicSiteSchema, { theme: "auto", customCss: css }));
  expect(document.querySelectorAll("img")).toHaveLength(0);
  expect((document.head.lastElementChild as HTMLStyleElement).textContent).toBe(css);
  expect(document.body.dataset.pwned).toBeUndefined();
});
