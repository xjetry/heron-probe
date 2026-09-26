import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithService } from "../test/harness";
import { NodePage } from "./NodePage";

vi.mock("../components/Chart", () => ({
  Chart: ({ labels }: { labels: string[] }) => <div data-testid="chart">{labels.map((l) => <span key={l}>{l}</span>)}</div>,
}));

const snapshot = async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "edge-1", online: true, facts: { os: "Alpine 3.21", arch: "arm64", cpuModel: "Neoverse", cpuCores: 2 } }] });

it("公开节点的历史图表走 PublicService，与面板同一组时间范围与探测图例", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  const queryProbes = vi.fn(async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [{ taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443" }] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  expect(await screen.findByRole("heading", { level: 1, name: "edge-1" })).toBeInTheDocument();
  expect(await screen.findAllByText("TCP example.com:443")).toHaveLength(2);
  expect(screen.getAllByTestId("chart")).toHaveLength(9);
  for (const r of ["1h", "6h", "24h", "7d", "30d"]) expect(screen.getByRole("button", { name: r })).toBeInTheDocument();
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect((queryMetrics.mock.calls[0] as unknown[])[0]).toMatchObject({ nodeId: 7n, maxPoints: 1000 });
});

it("快照里没有的节点说明不存在或未公开，也不去查历史", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/9");
  expect(await screen.findByRole("alert")).toHaveTextContent("节点 9 不存在或未公开");
  expect(queryMetrics).not.toHaveBeenCalled();
});
