import { expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { GetStorageStatsResponseSchema } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin } from "../test/harness";
import { Storage } from "./Storage";

const routes = [{ path: "/storage", Component: Storage }];
const at = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString();

const stats = create(GetStorageStatsResponseSchema, {
  dbBytes: 4096n,
  tables: [{ name: "metric_1m", rows: 3n }],
  lastRollupAt: 1_767_225_000n,
  series: [
    // 数值本身远在阈值之外，但 hub 说不标红：面板只看结论，不自己拿数值重算。
    { table: "metric_1m", bucketS: 60, retentionS: 604_800n, oldestTs: 1n, oldestStale: false },
    { table: "metric_5m", bucketS: 300, retentionS: 2_592_000n, oldestTs: 1_767_000_000n, oldestStale: true, watermarkTs: 1_767_100_000n, watermarkStale: true },
    { table: "metric_1h", bucketS: 3600, retentionS: 31_536_000n, watermarkTs: 1_767_222_000n },
  ],
});

const row = async (table: string) => {
  const region = await screen.findByRole("region", { name: "时序表健康" });
  return within(within(region).getByRole("cell", { name: table }).closest("tr")!);
};

it("按 hub 给的 stale 标红，不按数值重算", async () => {
  renderWithAdmin({ getStorageStats: async () => stats }, routes, "/storage");
  const m1 = await row("metric_1m");
  expect(m1.getByText(at(1n))).not.toHaveClass("error");
  const m5 = await row("metric_5m");
  expect(m5.getByText(`${at(1_767_000_000n)}（超出保留期）`)).toHaveClass("error");
  expect(m5.getByText(`${at(1_767_100_000n)}（上卷滞后）`)).toHaveClass("error");
  const h1 = await row("metric_1h");
  expect(h1.getByText(at(1_767_222_000n))).not.toHaveClass("error");
});

it("缺失的读数按各自含义显示：空表、没有水位、从未成功", async () => {
  renderWithAdmin({ getStorageStats: async () => stats }, routes, "/storage");
  const m1 = await row("metric_1m");
  expect(m1.getByText("—")).toBeInTheDocument();
  const h1 = await row("metric_1h");
  expect(h1.getByText("空表")).toBeInTheDocument();
  expect(screen.getByText(/上次清理完成/)).toHaveTextContent(`上次清理完成：从未成功；上次上卷完成：${at(1_767_225_000n)}`);
});
