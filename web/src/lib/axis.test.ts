import { expect, it } from "vitest";
import { axisValues } from "./axis";

it.each([
  { unit: "bytes", values: [40, 40.2, 40.4].map((v) => v * 1024 ** 3) },
  { unit: "bytes/s", values: [40, 40.2, 40.4].map((v) => v * 1024 ** 2) },
  { unit: "count", values: [0, 0.5, 1] },
  { unit: "percent", values: [40, 40.2, 40.4] },
])("$unit 的相邻轴刻度不重名", ({ unit, values }) => {
  expect(new Set(axisValues(values, unit)).size).toBe(values.length);
});

it("无单位轴按刻度间隔保留小数且不添加后缀", () => {
  expect(axisValues([0, 0.2, 0.4], "")).toEqual(["0.0", "0.2", "0.4"]);
});

it("bytes/s 轴用同一倍率并带 /s 后缀", () => {
  expect(axisValues([0, 1024, 2048], "bytes/s")).toEqual(["0 KiB/s", "1 KiB/s", "2 KiB/s"]);
});
