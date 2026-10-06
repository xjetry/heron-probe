import { expect, it } from "vitest";
import type { ProbeSample } from "../gen/heron/v1/query_pb";
import { assembleComparison, chunkNodeIds, taskIdParam, type ComparisonChunk } from "./probeComparison";

const names = new Map<bigint, string>([
  [1n, "edge"],
  [2n, "edge"],
  [3n, "other"],
  [4n, "gone"],
  [5n, "pending"],
]);

function sample(ts: number, sent: number, lost: number, errors: number, rttMeanUs?: number): ProbeSample {
  return { ts: BigInt(ts), sent, lost, errors, rttMeanUs } as ProbeSample;
}

const from = 100;
const to = 300;

function chunk(series: ComparisonChunk["series"], unavailable: bigint[] = [], stepS = 60): ComparisonChunk {
  return { stepS, series, unavailableNodeIds: unavailable };
}

it("任务编号只接受十进制正整数", () => {
  expect(taskIdParam("12")).toBe(12n);
  expect(taskIdParam("0")).toBeUndefined();
  expect(taskIdParam("01")).toBeUndefined();
  expect(taskIdParam(undefined)).toBeUndefined();
});

it("块大小只取调用方传入的上限，0 与负数不是不限，也不退回固定块大小", () => {
  expect(chunkNodeIds([1n, 2n, 3n, 4n, 5n], 2)).toEqual({ ok: true, chunks: [[1n, 2n], [3n, 4n], [5n]] });
  expect(chunkNodeIds([1n, 2n, 3n, 4n, 5n], 4).ok && chunkNodeIds([1n, 2n, 3n, 4n, 5n], 4)).toEqual({
    ok: true, chunks: [[1n, 2n, 3n, 4n], [5n]],
  });
  expect(chunkNodeIds([1n, 2n, 3n], 0)).toEqual({ ok: false, reason: "non-positive-limit" });
  expect(chunkNodeIds([1n, 2n, 3n], -1)).toEqual({ ok: false, reason: "non-positive-limit" });
});

it("全超时 100%、全本地错误 0%、混合按 lost/sent；三者都没有 RTT，不把 null 画成 0", () => {
  const view = assembleComparison([1n, 2n, 3n], names, [chunk([
    { nodeId: 1n, samples: [sample(120, 10, 10, 0)] },
    { nodeId: 2n, samples: [sample(120, 5, 0, 5)] },
    { nodeId: 3n, samples: [sample(120, 10, 4, 3)] },
  ])], from, to);
  expect(view.complete).toBe(true);
  expect(view.loss.labels).toEqual(["edge #1", "edge #2", "other"]);
  expect(view.loss.data[0]).toEqual([60, 120, 180, 240]);
  expect(view.loss.data.slice(1).map((col) => col[1])).toEqual([100, 0, 40]);
  expect(view.loss.missing).toEqual([]);
  expect(view.rtt.labels).toEqual([]);
  expect(view.rtt.data.slice(1)).toEqual([]);
  expect(view.rtt.missing).toEqual(["edge #1", "edge #2", "other"]);
  expect(view.unavailable).toEqual([]);
});

it("有 RTT 的节点才进 RTT 图；不可见节点单列，不进两张图的线或无读数名单", () => {
  const view = assembleComparison([1n, 2n, 4n], names, [chunk([
    { nodeId: 1n, samples: [sample(120, 10, 1, 0, 2_500)] },
    { nodeId: 2n, samples: [] },
  ], [4n])], from, to);
  expect(view.complete).toBe(true);
  expect(view.loss.labels).toEqual(["edge #1"]);
  expect(view.loss.data[1]).toEqual([null, 10, null, null]);
  expect(view.loss.missing).toEqual(["edge #2"]);
  expect(view.rtt.labels).toEqual(["edge #1"]);
  expect(view.rtt.data[1]).toEqual([null, 2.5, null, null]);
  expect(view.rtt.missing).toEqual(["edge #2"]);
  expect(view.unavailable).toEqual(["gone"]);
});

it("还没返回的块里的节点不算无结果，整次对比也不算齐", () => {
  const view = assembleComparison([1n, 2n, 5n], names, [chunk([
    { nodeId: 1n, samples: [] },
    { nodeId: 2n, samples: [sample(120, 10, 10, 0)] },
  ])], from, to);
  expect(view.complete).toBe(false);
  expect(view.loss.missing).toEqual(["edge #1"]);
  expect(view.rtt.missing).toEqual(["edge #1", "edge #2"]);
  expect(view.loss.labels).toEqual(["edge #2"]);
  expect([...view.loss.missing, ...view.rtt.missing, ...view.loss.labels, ...view.unavailable].join(" ")).not.toContain("pending");
});
