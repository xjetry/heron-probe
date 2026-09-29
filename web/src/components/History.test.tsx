import { create } from "@bufbuild/protobuf";
import { screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AlignedData } from "uplot";
import { PublicService } from "../gen/probe/v1/public_pb";
import { type QueryMetricsRequest, QueryMetricsResponseSchema } from "../gen/probe/v1/query_pb";
import { NodeDetail } from "../pages/NodeDetail";
import { NodePage } from "../public/NodePage";
import { renderWithAdmin, renderWithService } from "../test/harness";

vi.mock("./Chart", () => ({
  Chart: ({ labels, unit, data }: { labels: string[]; unit: string; data: AlignedData }) => (
    <div data-testid="chart" data-labels={labels.join(",")} data-unit={unit}
      data-points={JSON.stringify(data.slice(1).map((column) => column.slice(0, 5)))} />
  ),
}));

afterEach(() => vi.restoreAllMocks());

const queryMetrics = async (req: QueryMetricsRequest) => {
  const start = Number(req.from) - Number(req.from) % 60;
  return create(QueryMetricsResponseSchema, {
    level: "1m", stepS: 60, ts: [0, 60, 120, 180].map((offset) => BigInt(start + offset)),
    series: [
      { name: "cpu", unit: "percent", samples: [{ n: 3, mean: 25, max: 92 }, { n: 1, mean: 0, max: 0 }, { n: 1, mean: 50 }, { n: 0, mean: 12, max: 99 }] },
      { name: "mem_used", unit: "bytes", samples: [{ n: 3, mean: 100, max: 300 }, { n: 1, mean: 0, max: 0 }, { n: 1, mean: 8 }, { n: 0, mean: 12, max: 99 }] },
      { name: "swap_used", unit: "bytes", samples: [{ n: 3, mean: 10, max: 30 }, { n: 1, mean: 0, max: 0 }, { n: 1, mean: 2 }, { n: 0, mean: 12, max: 99 }] },
      { name: "rx_bytes", unit: "bytes", samples: [{ n: 3, sum: 120 }, { n: 1, sum: 0 }, { n: 1, sum: 60 }, { n: 0, sum: 999 }] },
      { name: "tx_bytes", unit: "bytes", samples: [{ n: 3, sum: 180 }, { n: 1, sum: 0 }, { n: 1, sum: 60 }, { n: 0, sum: 999 }] },
      { name: "net_rx_bps", unit: "bytes/s", samples: [{ n: 3, mean: 4, max: 11 }, { n: 1, mean: 0, max: 0 }, { n: 1, mean: 6 }, { n: 0, mean: 7, max: 99 }] },
      { name: "net_tx_bps", unit: "bytes/s", samples: [{ n: 3, mean: 9, max: 17 }, { n: 1, mean: 0, max: 0 }, { n: 1, mean: 6 }, { n: 0, mean: 7, max: 99 }] },
    ],
  });
};

it.each([
  { surface: "管理", networkPeaks: true },
  { surface: "公开", networkPeaks: true },
  { surface: "管理", networkPeaks: false },
  { surface: "公开", networkPeaks: false },
])("$surface 节点均值与峰值不混用（含网络峰值系列：$networkPeaks）", async ({ surface, networkPeaks }) => {
  vi.spyOn(Date, "now").mockReturnValue(86_400_000);
  const queries = {
    queryMetrics: async (req: QueryMetricsRequest) => {
      const response = await queryMetrics(req);
      if (!networkPeaks) response.series = response.series.filter((series) => !series.name.startsWith("net_"));
      return response;
    },
    queryProbes: async () => ({ level: "1m", stepS: 60, series: [] }),
  };
  if (surface === "管理") {
    renderWithAdmin({ ...queries, listNodes: async () => ({ nodes: [{ id: 7n, name: "node" }] }), getTraffic: async () => ({ nodes: [] }) },
      [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  } else {
    renderWithService(PublicService, { ...queries, getSnapshot: async () => ({ nodes: [{ id: 7n, name: "node" }] }) },
      [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  }
  await screen.findByText(/级别 1m，每点 60s/);
  const expectedPanels = [
    { title: "CPU", labels: "CPU 均值,CPU 峰值", unit: "percent", points: [[25, 0, 50, null, null], [92, 0, null, null, null]] },
    { title: "内存 / 交换", labels: "内存均值,内存峰值,交换均值", unit: "bytes", points: [[100, 0, 8, null, null], [300, 0, null, null, null], [10, 0, 2, null, null]] },
    { title: "网络", labels: "下行均值,上行均值,下行峰值,上行峰值", unit: "bytes/s", points: [
      [2, 0, 1, null, null], [3, 0, 1, null, null],
      networkPeaks ? [11, 0, null, null, null] : [null, null, null, null, null],
      networkPeaks ? [17, 0, null, null, null] : [null, null, null, null, null],
    ] },
  ];
  const panels = expectedPanels.map((expected) => {
    const panel = screen.getByRole("heading", { name: expected.title }).parentElement!;
    const chart = within(panel).getByTestId("chart");
    return { title: expected.title, labels: chart.dataset.labels, unit: chart.dataset.unit, points: JSON.parse(chart.dataset.points!) };
  });
  expect(panels).toEqual(expectedPanels);
  expect(screen.getByText("峰值为每个图表时间桶内已采集样本的最大值，不代表采样间隔内的瞬时最高值；缺少峰值时留空。")).toBeInTheDocument();
});
