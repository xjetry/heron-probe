import { describe, expect, it } from "vitest";
import { sameTag, withTag, withoutTag } from "./tags";

describe("tags", () => {
  it.each([
    ["db", "DB", true], [" db ", "Db", true], ["Σ", "ς", true], ["K", "k", true],
    ["ß", "ss", false], ["db", "d", false], ["a.b", "axb", false], ["客户A", "客户a", true],
  ])("sameTag(%j, %j) = %s", (a, b, same) => {
    expect(sameTag(a, b)).toBe(same);
  });

  it("withTag 去首尾空白、跳过空串与折叠后重复，保留已有写法", () => {
    expect(withTag(["db"], "  web ")).toEqual(["db", "web"]);
    expect(withTag(["db"], "DB")).toEqual(["db"]);
    expect(withTag(["db"], "   ")).toEqual(["db"]);
  });

  it("withoutTag 按折叠后的名字移除", () => {
    expect(withoutTag(["db", "web"], "DB")).toEqual(["web"]);
  });
});
