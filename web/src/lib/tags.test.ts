import { describe, expect, it } from "vitest";
import { matchesTags, nextSelection, presentTags, sameTag, withTag, withoutTag } from "./tags";

describe("tags", () => {
  it.each([
    ["db", "DB", true], [" db ", "Db", true], ["Σ", "ς", true], ["K", "k", true],
    ["ß", "ss", false], ["db", "d", false], ["a.b", "axb", false], ["客户A", "客户a", true],
    ["web server", "Web Server", true], ["web server", "webserver", false],
    // 首尾空白与 hub 的 strings.TrimSpace 同一字符集：U+0085 去掉，U+FEFF 不去。
    ["\u0085db\u0085", "db", true], ["\uFEFFdb", "db", false], ["\u3000db", "db", true],
  ])("sameTag(%j, %j) = %s", (a, b, same) => {
    expect(sameTag(a, b)).toBe(same);
  });

  it("withTag 去首尾空白、跳过空串与折叠后重复，保留已有写法", () => {
    expect(withTag(["db"], "  web ")).toEqual(["db", "web"]);
    expect(withTag(["db"], " web server ")).toEqual(["db", "web server"]);
    expect(withTag(["web server"], "WEB SERVER")).toEqual(["web server"]);
    expect(withTag(["db"], "DB")).toEqual(["db"]);
    expect(withTag(["db"], "   ")).toEqual(["db"]);
    expect(withTag([], "\u0085web\u0085")).toEqual(["web"]);
    expect(withTag([], "\uFEFFweb")).toEqual(["\uFEFFweb"]);
  });

  it("withoutTag 按折叠后的名字移除", () => {
    expect(withoutTag(["db", "web"], "DB")).toEqual(["web"]);
  });
  describe("nextSelection", () => {
    it("单击：未选时变成只选这一个，覆盖其余", () => {
      expect(nextSelection([], "db", false)).toEqual(["db"]);
      expect(nextSelection(["web"], "db", false)).toEqual(["db"]);
      expect(nextSelection(["web", "db"], "cache", false)).toEqual(["cache"]);
    });
    it("单击：多选之一时收成只选它；恰好只选它时清空回到全部", () => {
      expect(nextSelection(["web", "db"], "db", false)).toEqual(["db"]);
      expect(nextSelection(["db"], "db", false)).toEqual([]);
      expect(nextSelection(["db"], "DB", false)).toEqual([]);
    });
    it("Shift+单击：其余不动，只翻转被点的一个", () => {
      expect(nextSelection([], "db", true)).toEqual(["db"]);
      expect(nextSelection(["web"], "db", true)).toEqual(["web", "db"]);
      expect(nextSelection(["web", "db"], "web", true)).toEqual(["db"]);
      expect(nextSelection(["db"], "DB", true)).toEqual([]);
    });
  });

  describe("matchesTags", () => {
    it("空选择匹配一切，含没有标签的节点", () => {
      expect(matchesTags([], [])).toBe(true);
      expect(matchesTags(["db"], [])).toBe(true);
    });
    it("多选取交集，按折叠比较", () => {
      expect(matchesTags(["db", "web"], ["web", "db"])).toBe(true);
      expect(matchesTags(["db"], ["web", "db"])).toBe(false);
      expect(matchesTags(["DB"], ["db"])).toBe(true);
      expect(matchesTags([], ["db"])).toBe(false);
    });
  });

  describe("presentTags", () => {
    it("跨节点按折叠去重，保留先出现的写法，按码元序排序", () => {
      expect(presentTags([{ tags: ["web", "db"] }, { tags: ["DB", "cache"] }, { tags: [] }])).toEqual(["cache", "db", "web"]);
      expect(presentTags([{ tags: ["DB"] }, { tags: ["db"] }])).toEqual(["DB"]);
      expect(presentTags([])).toEqual([]);
    });
  });
});
