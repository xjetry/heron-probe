import { create } from "@bufbuild/protobuf";
import { QueryMetricsResponseSchema } from "../gen/probe/v1/query_pb";
import { describe, expect, it } from "vitest";
import { toAligned, unitOf } from "./series";

const resp = create(QueryMetricsResponseSchema, {
  level: "1m",
  stepS: 180,
  ts: [0n, 180n, 540n],
  series: [
    { name: "rx_bytes", unit: "bytes", samples: [{ n: 2, sum: 3600 }, { n: 0 }, { n: 1, sum: 180 }] },
    { name: "cpu", unit: "percent", samples: [{ n: 3, mean: 1, max: 2 }, { n: 3, mean: 2, max: 3 }, { n: 1, mean: 9, max: 9 }] },
    { name: "swap_used", unit: "bytes", samples: [{ n: 0, mean: 0 }, { n: 0, mean: 0 }, { n: 0, mean: 0 }] },
  ],
});

describe("toAligned", () => {
  it("可加量样本取 sum / step 得到速率；n=0 仍是 null", () => {
    const [, rx] = toAligned(resp, [{ name: "rx_bytes", value: "sum-rate", label: "下行均值" }], 30, 720);
    expect(rx).toEqual([20, null, null, 1]);
  });
  it("按 step 补全网格；缺桶与 n=0 都是 null", () => {
    const [xs, cpu, swap] = toAligned(resp, [
      { name: "cpu", value: "mean", label: "CPU 均值" },
      { name: "swap_used", value: "mean", label: "交换均值" },
    ], 30, 720);
    expect(xs).toEqual([0, 180, 360, 540]);
    expect(cpu).toEqual([1, 2, null, 9]);
    expect(swap).toEqual([null, null, null, null]);
  });
  it("未知指标整列 null，单位为空串", () => {
    const [, ghost] = toAligned(resp, [{ name: "ghost", value: "max", label: "未知峰值" }], 0, 360);
    expect(ghost).toEqual([null, null]);
    expect(unitOf(resp, "ghost")).toBe("");
    expect(unitOf(resp, "cpu")).toBe("percent");
  });
  it("有样本的零均值仍是零", () => {
    const zero = create(QueryMetricsResponseSchema, {
      stepS: 60, ts: [0n],
      series: [{ name: "cpu", samples: [{ n: 1, mean: 0 }] }],
    });
    expect(toAligned(zero, [{ name: "cpu", value: "mean", label: "CPU 均值" }], 0, 60)[1]).toEqual([0]);
  });
  it("同一指标能分别选择均值和峰值，重复名字不合并列", () => {
    const [, mean, max] = toAligned(resp, [
      { name: "cpu", value: "mean", label: "CPU 均值" },
      { name: "cpu", value: "max", label: "CPU 峰值" },
    ], 30, 720);
    expect(mean).toEqual([1, 2, null, 9]);
    expect(max).toEqual([2, 3, null, 9]);
  });
  it.each([
    { value: "mean", expected: 7 },
    { value: "sum-rate", expected: 3 },
    { value: "max", expected: 19 },
  ] as const)("$value 只读取指定统计量，不根据指标名称或其它字段猜测", ({ value, expected }) => {
    const mixed = create(QueryMetricsResponseSchema, {
      stepS: 60, ts: [0n],
      series: [{ name: "custom", samples: [{ n: 1, mean: 7, sum: 180, max: 19 }] }],
    });
    expect(toAligned(mixed, [{ name: "custom", value, label: "自定义" }], 0, 60)[1]).toEqual([expected]);
  });
  it.each(["mean", "sum-rate", "max"] as const)("%s 缺失不借用其它统计量；无样本不画，合法零值保留", (value) => {
    const samples = create(QueryMetricsResponseSchema, {
      stepS: 60, ts: [0n, 60n, 120n],
      series: [{ name: "custom", samples: [
        { n: 1, mean: 7, sum: 180, max: 19, [value === "sum-rate" ? "sum" : value]: undefined },
        { n: 0, mean: 7, sum: 180, max: 19 },
        { n: 1, mean: 0, sum: 0, max: 0 },
      ] }],
    });
    expect(toAligned(samples, [{ name: "custom", value, label: "自定义" }], 0, 240)[1]).toEqual([null, null, 0, null]);
  });
});
