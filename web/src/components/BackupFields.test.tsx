import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import type { UpdateSettingsRequest } from "../gen/probe/v1/admin_pb";
import { Appearance } from "../pages/Appearance";
import { renderWithAdmin } from "../test/harness";

const current = { title: "机房", theme: "auto", backup: { endpoint: "https://s3.example", bucket: "private-backups", region: "auto", accessKey: "access", prefix: "hub/", configIntervalS: 300, metricsIntervalS: 86400, configKeep: 48, metricsKeep: 14, channels: [7n] } };

it("备份区块保存设置，secret 只写且下次保存缺席，关闭使用空 endpoint", async () => {
  const sent: UpdateSettingsRequest[] = [];
  renderWithAdmin({
    getSettings: async () => ({ settings: current }),
    listNotifyChannels: async () => ({ channels: [{ id: 7n, name: "运维" }] }),
    updateSettings: async (req) => { sent.push(req); return { settings: { ...req.settings!, backup: { ...req.settings!.backup!, secret: undefined } } }; },
  }, [{ path: "/appearance", Component: Appearance }], "/appearance");
  const form = within(await screen.findByRole("form", { name: "公开页外观" }));
  const toggle = form.getByText("备份到 S3");
  fireEvent.click(toggle);
  const secret = form.getByLabelText("Secret");
  expect(secret).toHaveValue("");
  expect(secret).toHaveAttribute("type", "password");
  expect(form.getByLabelText("配置周期（秒）")).toHaveValue(300);
  expect(form.getByLabelText("指标周期（秒）")).toHaveValue(86400);
  expect(await form.findByLabelText("运维")).toBeChecked();
  fireEvent.change(secret, { target: { value: "new-secret" } });
  fireEvent.change(form.getByLabelText("配置保留份数"), { target: { value: "50" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent[0].settings?.backup).toMatchObject({ secret: "new-secret", configKeep: 50, channels: [7n] });
  expect(secret).toHaveValue("");
  fireEvent.change(form.getByLabelText("Endpoint"), { target: { value: "" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1].settings?.backup?.secret).toBeUndefined();
  expect(sent[1].settings?.backup?.endpoint).toBe("");
});

it("备份周期越界时不发送保存请求，恢复有效边界后可保存", async () => {
  const sent: UpdateSettingsRequest[] = [];
  renderWithAdmin({ getSettings: async () => ({ settings: current }), listNotifyChannels: async () => ({ channels: [] }), updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } }, [{ path: "/appearance", Component: Appearance }], "/appearance");
  const element = await screen.findByRole("form", { name: "公开页外观" });
  const form = within(element);
  fireEvent.click(form.getByText("备份到 S3"));
  const field = form.getByLabelText("配置周期（秒）");
  fireEvent.change(field, { target: { value: "0" } });
  fireEvent.submit(element);
  fireEvent.change(field, { target: { value: "60" } });
  fireEvent.submit(element);
  await form.findByRole("status");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings?.backup?.configIntervalS).toBe(60);
});
