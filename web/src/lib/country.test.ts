import { expect, it } from "vitest";
import { flag } from "./country";

it("两个大写字母换成对应的区域指示符", () => {
  expect(flag("US")).toBe("\u{1F1FA}\u{1F1F8}");
  expect(flag("JP")).toBe("\u{1F1EF}\u{1F1F5}");
  expect(flag("AZ")).toBe("\u{1F1E6}\u{1F1FF}");
});

it.each(["", "us", "USA", "U", "U1", "ÜS"])("其它形状 %j 没有旗帜", (code) => {
  expect(flag(code)).toBe("");
});
