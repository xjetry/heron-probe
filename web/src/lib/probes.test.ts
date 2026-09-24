import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ProbeTaskDetailSchema, QueryProbesResponseSchema } from "../gen/probe/v1/admin_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { lossPercent, rttMeanMs, taskIdsOf, taskLabel, toProbeAligned } from "./probes";

const resp = create(QueryProbesResponseSchema, {
  level: "1m", stepS: 60,
  series: [
    { taskId: 3n, samples: [
      { ts: 120n, sent: 10, lost: 2, errors: 0, rttMeanUs: 12_500 },
      { ts: 240n, sent: 10, lost: 10, errors: 0 },
    ] },
    { taskId: 7n, samples: [{ ts: 180n, sent: 5, lost: 0, errors: 5 }] },
  ],
});

describe("toProbeAligned", () => {
  it("每个任务一列，缺 ts 与缺 rtt 都是 null", () => {
    const loss = toProbeAligned(resp, [3n, 7n], 100, 300, lossPercent);
    expect(loss[0]).toEqual([60, 120, 180, 240]);
    expect(loss[1]).toEqual([null, 20, null, 100]);
    expect(loss[2]).toEqual([null, null, 0, null]);
    const rtt = toProbeAligned(resp, [3n, 7n], 100, 300, rttMeanMs);
    expect(rtt[1]).toEqual([null, 12.5, null, null]);
    expect(rtt[2]).toEqual([null, null, null, null]);
  });
  it("全部失败的点丢包率为 0、rtt 为空：error 不计入丢包", () => {
    const s = resp.series[1].samples[0];
    expect(lossPercent(s)).toBe(0);
    expect(rttMeanMs(s)).toBeNull();
  });
  it("taskIdsOf 保持响应顺序", () => {
    expect(taskIdsOf(resp)).toEqual([3n, 7n]);
  });
});

describe("taskLabel", () => {
  const tasks = [create(ProbeTaskDetailSchema, { task: { id: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443", intervalS: 30, timeoutMs: 1000 }, nodeIds: [] })];
  it("有任务时用类型与目标", () => expect(taskLabel(3n, tasks)).toBe("TCP 1.1.1.1:443"));
  it("找不到任务（已删除）时退回编号", () => expect(taskLabel(7n, tasks)).toBe("任务 #7"));
  it("任务列表未到时也退回编号", () => expect(taskLabel(3n, undefined)).toBe("任务 #3"));
});
