import { Code, ConnectError } from "@connectrpc/connect";
import { screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Appearance } from "../pages/Appearance";

const routes = [{ path: "/appearance", Component: Appearance }];

it("设置页按层显示启用、上次成功和故障，保留从未成功的缺席语义", async () => {
  renderWithAdmin({
    getSettings: async () => ({}),
    getBackupStatus: async () => ({ enabled: true, config: { lastSuccessAt: 0n, failure: { category: "upload/http_status", sinceAt: 60n, statusCode: 503 } }, metrics: {} }),
  }, routes, "/appearance");
  const region = within(await screen.findByRole("region", { name: "备份状态" }));
  expect(region.getByText("自动备份已启用")).toBeVisible();
  const config = within(region.getByRole("article", { name: "配置与凭据" }));
  expect(config.getByText(`上次成功：${new Date(0).toLocaleString()}`)).toBeVisible();
  expect(config.getByText(`当前故障：upload/http_status（HTTP 503）；自 ${new Date(60000).toLocaleString()} 起`)).toHaveClass("error");
  const metrics = within(region.getByRole("article", { name: "指标与探测历史" }));
  expect(metrics.getByText("上次成功：从未成功")).toBeVisible();
  expect(metrics.getByText("无当前故障")).toBeVisible();
});

it("未启用时仍显示指标层故障与历史成功", async () => {
  renderWithAdmin({ getSettings: async () => ({}), getBackupStatus: async () => ({ enabled: false, config: {}, metrics: { lastSuccessAt: 60n, failure: { category: "snapshot", sinceAt: 120n } } }) }, routes, "/appearance");
  const region = within(await screen.findByRole("region", { name: "备份状态" }));
  expect(region.getByText("自动备份未启用")).toBeVisible();
  const metrics = within(region.getByRole("article", { name: "指标与探测历史" }));
  expect(metrics.getByText(`上次成功：${new Date(60000).toLocaleString()}`)).toBeVisible();
  expect(metrics.getByText(`当前故障：snapshot；自 ${new Date(120000).toLocaleString()} 起`)).toHaveClass("error");
});

it("状态查询失败不冒充备份正常，也不阻塞设置表单", async () => {
  renderWithAdmin({ getSettings: async () => ({}), getBackupStatus: async () => { throw new ConnectError("backup status failed", Code.Internal); } }, routes, "/appearance");
  expect(await screen.findByText(/backup status failed/)).toBeVisible();
  expect(screen.queryByText("无当前故障")).toBeNull();
  expect(screen.getByRole("form", { name: "公开页外观" })).toBeVisible();
});
