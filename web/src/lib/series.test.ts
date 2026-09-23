import { create } from "@bufbuild/protobuf";
import { QueryMetricsResponseSchema } from "../gen/probe/v1/admin_pb";
import { describe, expect, it } from "vitest";
import { toAligned, unitOf } from "./series";

const resp = create(QueryMetricsResponseSchema, {
  level: "1m",
  stepS: 180,
  ts: [0n, 180n, 540n],
  series: [
    { name: "cpu", unit: "percent", samples: [{ n: 3, mean: 1, max: 2 }, { n: 3, mean: 2, max: 3 }, { n: 1, mean: 9, max: 9 }] },
    { name: "swap_used", unit: "bytes", samples: [{ n: 0, mean: 0 }, { n: 0, mean: 0 }, { n: 0, mean: 0 }] },
  ],
});

describe("toAligned", () => {
  it("按 step 补全网格；缺桶与 n=0 都是 null", () => {
    const [xs, cpu, swap] = toAligned(resp, ["cpu", "swap_used"], 30, 720);
    expect(xs).toEqual([0, 180, 360, 540]);
    expect(cpu).toEqual([1, 2, null, 9]);
    expect(swap).toEqual([null, null, null, null]);
  });
  it("未知指标整列 null，单位为空串", () => {
    const [, ghost] = toAligned(resp, ["ghost"], 0, 360);
    expect(ghost).toEqual([null, null]);
    expect(unitOf(resp, "ghost")).toBe("");
    expect(unitOf(resp, "cpu")).toBe("percent");
  });
  it("有样本的零均值仍是零", () => {
    const zero = create(QueryMetricsResponseSchema, {
      stepS: 60, ts: [0n],
      series: [{ name: "cpu", samples: [{ n: 1, mean: 0 }] }],
    });
    expect(toAligned(zero, ["cpu"], 0, 60)[1]).toEqual([0]);
  });
});
