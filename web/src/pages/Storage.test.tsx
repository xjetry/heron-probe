import { expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { GetStorageStatsResponseSchema, WalFileObservationSchema, type WalFileObservation } from "../gen/heron/v1/admin_pb";
import { renderWithAdmin } from "../test/harness";
import { Storage } from "./Storage";

const routes = [{ path: "/storage", Component: Storage }];
const at = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString();

const stats = create(GetStorageStatsResponseSchema, {
  dbBytes: 4096n,
  sqlObservedAt: 1_767_220_000n,
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

it("显示 SQL 统计时刻与完成后的复用窗口", async () => {
  renderWithAdmin({ getStorageStats: async () => stats }, routes, "/storage");
  expect(await screen.findByText(/^统计于 /)).toHaveTextContent(`统计于 ${at(1_767_220_000n)}；同一份 SQL 统计在算出后 60 秒内复用。`);
});

it("旧 hub 未给 SQL 统计时刻时不伪造请求时刻", async () => {
  renderWal();
  await screen.findByText(/数据库逻辑大小/);
  expect(screen.queryByText(/^统计于 /)).toBeNull();
});

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

const observedAt = 1_767_230_000n;

// 逻辑大小固定为 4096（显示 4.0 KiB），避免和 WAL 的 0 B 混在同一段文本里。
const renderWal = (wal?: WalFileObservation) => {
  const response = create(GetStorageStatsResponseSchema, { dbBytes: 4096n });
  if (wal) response.wal = wal;
  renderWithAdmin({ getStorageStats: async () => response }, routes, "/storage");
};

it("旧 hub 没有 wal：不显示观测行，也不当成无文件或 0 字节", async () => {
  renderWal();
  expect(await screen.findByText(/数据库逻辑大小：4.0 KiB/)).toBeInTheDocument();
  expect(screen.getByText(/不含 -wal 与 -shm 文件/)).toHaveTextContent("不是同一时刻读出的");
  // 旧 hub 与 absent 不得共用“无 WAL 文件”：先钉这句，再钉整行不出现。
  expect(screen.queryByText(/无 WAL 文件/)).toBeNull();
  expect(screen.queryByText(/^WAL 文件：/)).toBeNull();
  expect(screen.queryByText(/0 B/)).toBeNull();
});

it("bytes 为 0 仍是文件长度，带观测时刻，不是无文件", async () => {
  renderWal(create(WalFileObservationSchema, { observedAt, result: { case: "bytes", value: 0n } }));
  const line = await screen.findByText(/^WAL 文件：/);
  expect(line.textContent).not.toContain("无 WAL 文件");
  expect(line).toHaveTextContent(`WAL 文件：0 B（观测于 ${at(observedAt)}）`);
  expect(screen.getByText(/不含 -wal 与 -shm 文件/)).toHaveTextContent("不是同一时刻读出的");
  // 长度旁边不出现危险或阈值判断；有长度的时候最容易被读成健康信号。
  expect(screen.queryByText(/越大越危险|超过|阈值/)).toBeNull();
});

it("absent 显示无 WAL 文件并带观测时刻，与旧 hub 不显示不是同一文案", async () => {
  renderWal(create(WalFileObservationSchema, { observedAt, result: { case: "absent", value: true } }));
  const line = await screen.findByText(/^WAL 文件：无 WAL 文件/);
  expect(line).toHaveTextContent(`WAL 文件：无 WAL 文件（观测于 ${at(observedAt)}）`);
  expect(screen.getByText(/实际长度/)).toBeInTheDocument();
});

it("error 显示大小未知，不出现 0 字节，也不写成无文件", async () => {
  renderWal(create(WalFileObservationSchema, { observedAt, result: { case: "error", value: "permission denied" } }));
  const line = await screen.findByText(/^WAL 文件：/);
  expect(line.textContent).not.toContain("0 B");
  expect(line.textContent).not.toContain("0 字节");
  expect(line.textContent).not.toContain("无 WAL 文件");
  expect(line).toHaveTextContent(`WAL 文件：大小未知（permission denied）（观测于 ${at(observedAt)}）`);
  expect(screen.queryByText(/0 B/)).toBeNull();
  expect(screen.queryByText(/0 字节/)).toBeNull();
});

it("没有 result 分支时显示未知，不补成 0 字节", async () => {
  renderWal(create(WalFileObservationSchema, { observedAt }));
  expect(await screen.findByText(/数据库逻辑大小/)).toBeInTheDocument();
  const line = screen.queryByText(/^WAL 文件：/);
  expect(line).not.toBeNull();
  expect(line?.textContent).not.toContain("0 B");
  expect(line?.textContent).not.toContain("0 字节");
  expect(line?.textContent).not.toContain("无 WAL 文件");
  expect(line?.textContent).not.toContain("大小未知");
  expect(line).toHaveTextContent(`WAL 文件：未知（观测于 ${at(observedAt)}）`);
});
