import { expect, it } from "vitest";
import { expiryLevel, nodeStatus, STATUS_LABEL, STATUS_ORDER, usageLevel } from "./status";

it.each([
  [{ online: true, maintenance: false, lastSeenAt: 10n }, "online"],
  [{ online: false, maintenance: false, lastSeenAt: 10n }, "offline"],
  [{ online: false, maintenance: false }, "never"],
  // 维护中优先：站长主动摘出的节点即便仍在上报也不算在线。
  [{ online: true, maintenance: true, lastSeenAt: 10n }, "maintenance"],
  [{ online: false, maintenance: true }, "maintenance"],
] as const)("nodeStatus(%o) = %s", (input, want) => {
  expect(nodeStatus(input)).toBe(want);
});

it("四种状态各有标签，顺序表覆盖全部四种且不重复", () => {
  expect(Object.keys(STATUS_LABEL).sort()).toEqual(["maintenance", "never", "offline", "online"]);
  expect([...STATUS_ORDER].sort()).toEqual(["maintenance", "never", "offline", "online"]);
  expect(STATUS_LABEL.never).toBe("从未上报");
});

it.each([
  [0, "neutral"], [69.9, "neutral"], [70, "attention"], [90, "attention"], [90.1, "critical"], [100, "critical"],
])("usageLevel(%d) = %s", (v, want) => {
  expect(usageLevel(v)).toBe(want);
});

it.each([
  [undefined, "neutral"], [31, "neutral"], [30, "attention"], [0, "attention"], [-1, "critical"],
])("expiryLevel(%s) = %s", (d, want) => {
  expect(expiryLevel(d)).toBe(want);
});
