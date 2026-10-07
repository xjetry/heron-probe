import { expect, it } from "vitest";
import { liveById } from "./adminStatus";
import { applyScope, isScoped, paramsWithScope, scopeFromParams } from "./nodeFilters";

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
