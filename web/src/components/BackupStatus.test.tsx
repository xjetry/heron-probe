import { Code, ConnectError } from "@connectrpc/connect";
import { screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Appearance } from "../pages/Appearance";
import { dateTime } from "../lib/format";

const routes = [{ path: "/appearance", Component: Appearance }];

it("设置页按层显示启用、上次成功和故障，保留从未成功的缺席语义", async () => {
  renderWithAdmin({
    getSettings: async () => ({}),
    getBackupStatus: async () => ({ enabled: true, config: { lastSuccessAt: 0n, failure: { category: "upload/http_status", sinceAt: 60n, statusCode: 503 } }, metrics: {} }),
  }, routes, "/appearance");
  const region = within(await screen.findByRole("region", { name: "备份状态" }));
  expect(region.getByText("自动备份已启用")).toBeVisible();
  const config = within(region.getByRole("article", { name: "配置与凭据" }));
  expect(config.getByText(`上次成功：${dateTime(0)}`)).toBeVisible();
  expect(config.getByText(`当前故障：upload/http_status（HTTP 503）；自 ${dateTime(60)} 起`)).toHaveClass("error");
  const metrics = within(region.getByRole("article", { name: "指标与探测历史" }));
  expect(metrics.getByText("上次成功：从未成功")).toBeVisible();
  expect(metrics.getByText("无当前故障")).toBeVisible();
});

it("设置读不出时 enabled 为 false，仍显示配置层 settings 故障与历史成功", async () => {
  renderWithAdmin({ getSettings: async () => ({}), getBackupStatus: async () => ({ enabled: false, config: { lastSuccessAt: 60n, failure: { category: "settings", sinceAt: 120n } }, metrics: { lastSuccessAt: 60n } }) }, routes, "/appearance");
  const region = within(await screen.findByRole("region", { name: "备份状态" }));
  expect(region.getByText("自动备份未启用")).toBeVisible();
  const config = within(region.getByRole("article", { name: "配置与凭据" }));
  expect(config.getByText(`上次成功：${dateTime(60)}`)).toBeVisible();
  expect(config.getByText(`当前故障：settings；自 ${dateTime(120)} 起`)).toHaveClass("error");
  const metrics = within(region.getByRole("article", { name: "指标与探测历史" }));
  expect(metrics.getByText(`上次成功：${dateTime(60)}`)).toBeVisible();
  expect(metrics.getByText("无当前故障")).toBeVisible();
  expect(region.getByText(/停用备份即结束两层的故障跟踪/)).toBeVisible();
});

it("状态查询失败不冒充备份正常，也不阻塞设置表单", async () => {
  renderWithAdmin({ getSettings: async () => ({}), getBackupStatus: async () => { throw new ConnectError("backup status failed", Code.Internal); } }, routes, "/appearance");
  expect(await screen.findByText(/backup status failed/)).toBeVisible();
  expect(screen.queryByText("无当前故障")).toBeNull();
  expect(screen.getByRole("form", { name: "公开页外观" })).toBeVisible();
});

it("缺包主题单列提示，主题上传故障给出可操作说明", async () => {
  renderWithAdmin({ getSettings: async () => ({}), getBackupStatus: async () => ({ themesWithoutPackage: ["old"], config: { failure: { category: "theme_upload/http_status", sinceAt: 1n } } }) }, routes, "/appearance");
  expect(await screen.findByText("主题 old 未备份：请重新上传原包")).toBeVisible();
  expect(screen.getByText("主题包上传失败，请检查对象存储连接、凭据和写入权限")).toBeVisible();
});
