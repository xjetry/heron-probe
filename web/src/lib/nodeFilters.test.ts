import { expect, it } from "vitest";
import { liveById } from "./adminStatus";
import { applyScope, canonicalTagFilter, isScoped, nodeListReturnPath, nodeListReturnState, paramsWithScope, paramsWithSearch, paramsWithTagFilter, scopeFromParams, searchFromParams, tagFilterFromParams } from "./nodeFilters";

it("URL 参数解析：合法值进入筛选，非法值被忽略而不是变成空列表", () => {
  expect(scopeFromParams(new URLSearchParams("status=offline&expiring=1&lagging=1"))).toEqual({ status: "offline", expiring: true, lagging: true });
  expect(scopeFromParams(new URLSearchParams("status=foo&expiring=yes&lagging=0"))).toEqual({ status: null, expiring: false, lagging: false });
  expect(isScoped(scopeFromParams(new URLSearchParams("")))).toBe(false);
});

it("回写只动自己的三个键，其余参数保留；空值删键", () => {
  const next = paramsWithScope(new URLSearchParams("tab=x&status=offline"), { status: null, expiring: true, lagging: false });
  expect(next.toString()).toBe("tab=x&expiring=1");
});

const nodes = [
  { id: 1n, name: "on", maintenance: false, facts: { agentVersion: "v0.7.0" }, billing: { daysLeft: 5 } },
  { id: 2n, name: "off", maintenance: false, facts: { agentVersion: "v0.8.0" } },
  { id: 3n, name: "unknown", maintenance: false },
  { id: 4n, name: "maint", maintenance: true },
] as never[];
const live = liveById([{ id: 1n, online: true, lastSeenAt: 1n }, { id: 2n, online: false, lastSeenAt: 1n }, { id: 4n, online: true, lastSeenAt: 1n }] as never);

it("按状态 / 到期 / 落后过滤；未知状态只在不按状态筛时出现；筛选可叠加搜索", () => {
  const names = (scope: Parameters<typeof applyScope>[3], search = "") => applyScope(nodes, live, "v0.8.0", scope, search).map((n: { name: string }) => n.name);
  expect(names({ status: null, expiring: false, lagging: false })).toEqual(["on", "off", "unknown", "maint"]);
  expect(names({ status: "offline", expiring: false, lagging: false })).toEqual(["off"]);
  expect(names({ status: "maintenance", expiring: false, lagging: false })).toEqual(["maint"]);
  expect(names({ status: null, expiring: true, lagging: false })).toEqual(["on"]);
  expect(names({ status: null, expiring: false, lagging: true })).toEqual(["on"]);
  expect(names({ status: "online", expiring: true, lagging: true }, "on")).toEqual(["on"]);
  expect(applyScope(nodes, live, undefined, { status: null, expiring: false, lagging: true }, "")).toEqual([]);
});

it("标签过滤与搜索词在 URL 里往返；无标签优先于 tag，空的 tag 丢弃；写入时保留其他参数", () => {
  expect(tagFilterFromParams(new URLSearchParams("tag=db&tag=&tag=prod"))).toEqual({ kind: "tags", names: ["db", "prod"] });
  expect(tagFilterFromParams(new URLSearchParams("tag=db&untagged=1"))).toEqual({ kind: "untagged" });
  expect(tagFilterFromParams(new URLSearchParams("untagged=yes"))).toEqual({ kind: "tags", names: [] });
  const base = new URLSearchParams("status=offline&tag=old&untagged=1");
  expect(paramsWithTagFilter(base, { kind: "tags", names: ["a", "b"] }).toString()).toBe("status=offline&tag=a&tag=b");
  expect(paramsWithTagFilter(base, { kind: "untagged" }).toString()).toBe("status=offline&untagged=1");
  expect(searchFromParams(new URLSearchParams("q=%E9%A6%99%E6%B8%AF"))).toBe("香港");
  expect(paramsWithSearch(new URLSearchParams("q=x&status=offline"), "").toString()).toBe("status=offline");
  expect(searchFromParams(paramsWithSearch(new URLSearchParams(), "a&b=c"))).toBe("a&b=c");
});

it("URL 里的标签按清单换成清单写法并去重，清单没有的原样保留；清单未到或无标签过滤时原样返回", () => {
  expect(canonicalTagFilter({ kind: "tags", names: ["DB", "db", "gone"] }, ["db", "prod"])).toEqual({ kind: "tags", names: ["db", "gone"] });
  expect(canonicalTagFilter({ kind: "tags", names: ["DB"] }, undefined)).toEqual({ kind: "tags", names: ["DB"] });
  expect(canonicalTagFilter({ kind: "untagged" }, ["db"])).toEqual({ kind: "untagged" });
});

it("返回节点列表的路径只认以 ? 开头的字符串 state，其余回到不带筛选的列表", () => {
  expect(nodeListReturnPath(nodeListReturnState("?q=a&tag=db"))).toBe("/nodes?q=a&tag=db");
  for (const state of [nodeListReturnState(""), { nodeListSearch: "//evil.example" }, { nodeListSearch: 1 }, "?q=a", null, undefined]) {
    expect(nodeListReturnPath(state)).toBe("/nodes");
  }
});

