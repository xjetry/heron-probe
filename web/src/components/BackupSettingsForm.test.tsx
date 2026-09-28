import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import type { UpdateSettingsRequest } from "../gen/probe/v1/admin_pb";
import { BackupSettingsForm } from "./BackupSettingsForm";
import { Appearance } from "../pages/Appearance";
import { renderWithAdmin, type AdminImpl } from "../test/harness";

const backup = { endpoint: "https://s3.example", bucket: "private-backups", region: "auto", accessKey: "access", prefix: "hub", configIntervalS: 300, metricsIntervalS: 86400, configKeep: 48, metricsKeep: 14, notify: { channelIds: [7n] }, hasSecret: true };
const saved = { title: "机房", theme: "dark", accentColor: "#123abc", logo: "", customCss: "body { margin: 0 }", backup };
const channels = { channels: [{ id: 7n, name: "运维" }, { id: 8n, name: "值班" }] };

const render = (impl: AdminImpl) => renderWithAdmin({ getSettings: async () => ({ settings: saved }), listNotifyChannels: async () => channels, ...impl }, [{ path: "/appearance", Component: Appearance }], "/appearance");
// 请求里的 settings 是消息对象；回显要拼成普通的初始化对象，嵌套的 backup 才会按初始化形态序列化。
const appearanceOf = (req: UpdateSettingsRequest) => {
  const s = req.settings!;
  return { title: s.title, theme: s.theme, accentColor: s.accentColor, logo: s.logo, customCss: s.customCss };
};
const backupForm = async () => within(await screen.findByRole("form", { name: "备份到 S3" }));
const appearanceForm = async () => within(await screen.findByRole("form", { name: "公开页外观" }));

it("通知渠道选满 16 后禁用未选项，取消后允许重选", async () => {
  render({ listNotifyChannels: async () => ({ channels: Array.from({ length: 17 }, (_, i) => ({ id: BigInt(i+1), name: `渠道${i+1}` })) }),
    getSettings: async () => ({ settings: { ...saved, backup: { ...backup, notify: { channelIds: [] } } } }) });
  const form = await backupForm();
  const boxes = form.getAllByRole("checkbox");
  for (const box of boxes.slice(0,16)) fireEvent.click(box);
  expect(form.getByText("最多选 16 个渠道")).toBeInTheDocument();
  expect(boxes[16]).toBeDisabled();
  expect(boxes[0]).toBeEnabled();
  fireEvent.click(boxes[0]);
  expect(boxes[16]).toBeEnabled();
});

it("备份保存只投影五项外观，即使输入对象带总闸也不提交", async () => {
  const sent: UpdateSettingsRequest[] = [];
  const full = { ...saved, publicEnabled: true };
  renderWithAdmin({ listNotifyChannels: async () => channels, updateSettings: async (req) => {
    sent.push(req);
    return { settings: req.settings };
  } }, [{ path: "/backup", Component: () => <BackupSettingsForm current={undefined} appearance={full} /> }], "/backup");
  const form = await backupForm();
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings?.publicEnabled).toBeUndefined();
  expect(appearanceOf(sent[0])).toEqual({ title: saved.title, theme: saved.theme, accentColor: saved.accentColor, logo: saved.logo, customCss: saved.customCss });
});

it("备份表单保存：secret 只写且下次缺席，渠道以 notify 提交，关闭用空 endpoint", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: { ...req.settings!, backup: { ...req.settings!.backup!, secret: undefined, hasSecret: true } } }; } });
  const form = await backupForm();
  const secret = form.getByLabelText("Secret");
  expect(secret).toHaveValue("");
  expect(secret).toHaveAttribute("type", "password");
  expect(form.getByLabelText("配置周期（秒）")).toHaveValue(300);
  expect(form.getByLabelText("指标周期（秒）")).toHaveValue(86400);
  expect(form.getByLabelText("运维（#7）")).toBeChecked();
  fireEvent.change(secret, { target: { value: "new-secret" } });
  fireEvent.change(form.getByLabelText("配置保留份数"), { target: { value: "50" } });
  fireEvent.click(form.getByLabelText("值班（#8）"));
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent[0].settings?.backup).toMatchObject({ secret: "new-secret", configKeep: 50, notify: { channelIds: [7n, 8n] } });
  expect(sent[0].settings?.backup?.hasSecret).toBe(false);
  expect(secret).toHaveValue("");
  fireEvent.change(form.getByLabelText("Endpoint"), { target: { value: "" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1].settings?.backup?.secret).toBeUndefined();
  expect(sent[1].settings?.backup?.endpoint).toBe("");
});

it("备份周期越界时不发送保存请求，恢复有效边界后可保存", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const form = await backupForm();
  const element = screen.getByRole("form", { name: "备份到 S3" });
  const field = form.getByLabelText("配置周期（秒）");
  fireEvent.change(field, { target: { value: "0" } });
  fireEvent.submit(element);
  fireEvent.change(field, { target: { value: "60" } });
  fireEvent.submit(element);
  await form.findByRole("status");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings?.backup?.configIntervalS).toBe(60);
});

it("Secret 旁显示 hub 是否已保存 secret", async () => {
  render({ getSettings: async () => ({ settings: { ...saved, backup: { ...backup, hasSecret: false } } }) });
  expect((await backupForm()).getByLabelText("Secret")).toHaveAccessibleDescription("Secret 未设置");
});

it("Secret 已保存时如实显示", async () => {
  render({});
  expect((await backupForm()).getByLabelText("Secret")).toHaveAccessibleDescription("Secret 已保存");
});

// 两个表单各管各的：只改外观时请求里没有 backup（hub 对缺席的 backup 不改）。
it("外观表单的保存不带 backup", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: { ...appearanceOf(req), backup } }; } });
  const form = await appearanceForm();
  fireEvent.change(form.getByLabelText("标题"), { target: { value: "新标题" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings?.title).toBe("新标题");
  expect(sent[0].settings?.backup).toBeUndefined();
});

// 备份表单带的外观是 hub 的已保存值，不是外观表单里还没保存的草稿。
it("备份表单的保存带已保存的外观，不带外观草稿", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  fireEvent.change((await appearanceForm()).getByLabelText("标题"), { target: { value: "未保存的标题" } });
  const form = await backupForm();
  fireEvent.change(form.getByLabelText("配置保留份数"), { target: { value: "40" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings).toMatchObject({ title: "机房", theme: "dark", accentColor: "#123abc", logo: "", customCss: "body { margin: 0 }" });
  expect(sent[0].settings?.backup?.configKeep).toBe(40);
});

// 外观保存在途时备份表单不能提交；外观保存完成、设置重新读到之后，备份保存带的是新外观。
it("两个表单的保存互斥，后一个带的是先一个保存之后的外观", async () => {
  const sent: UpdateSettingsRequest[] = [];
  let current = saved;
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  render({
    getSettings: async () => ({ settings: current }),
    updateSettings: async (req) => {
      sent.push(req);
      if (sent.length === 1) await gate;
      current = { ...appearanceOf(req), backup };
      return { settings: current };
    },
  });
  const appearance = await appearanceForm();
  const form = await backupForm();
  fireEvent.change(appearance.getByLabelText("标题"), { target: { value: "新标题" } });
  fireEvent.click(appearance.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(form.getByRole("button", { name: "保存" })).toBeDisabled());
  fireEvent.submit(screen.getByRole("form", { name: "备份到 S3" }));
  expect(sent).toHaveLength(1);
  release();
  await appearance.findByRole("status");
  await waitFor(() => expect(form.getByRole("button", { name: "保存" })).toBeEnabled());
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await form.findByRole("status");
  expect(sent).toHaveLength(2);
  expect(sent[1].settings?.title).toBe("新标题");
});

// 反方向：备份保存在途时外观表单不能提交；备份保存完成、设置重新读到之后，外观表单照常保存。
it("备份保存在途时外观表单不能提交", async () => {
  const sent: UpdateSettingsRequest[] = [];
  let current = saved;
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  render({
    getSettings: async () => ({ settings: current }),
    updateSettings: async (req) => {
      sent.push(req);
      if (sent.length === 1) await gate;
      current = { ...appearanceOf(req), backup };
      return { settings: current };
    },
  });
  const appearance = await appearanceForm();
  const form = await backupForm();
  fireEvent.change(form.getByLabelText("配置保留份数"), { target: { value: "40" } });
  fireEvent.click(form.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(appearance.getByRole("button", { name: "保存" })).toBeDisabled());
  fireEvent.submit(screen.getByRole("form", { name: "公开页外观" }));
  expect(sent).toHaveLength(1);
  release();
  await form.findByRole("status");
  await waitFor(() => expect(appearance.getByRole("button", { name: "保存" })).toBeEnabled());
  fireEvent.change(appearance.getByLabelText("标题"), { target: { value: "新标题" } });
  fireEvent.click(appearance.getByRole("button", { name: "保存" }));
  await appearance.findByRole("status");
  expect(sent.map((r) => r.settings?.title)).toEqual(["机房", "新标题"]);
});

// 渠道列表读不到时不渲染备份表单：拿空列表求交会把已选渠道作为显式空集合提交，关掉备份失败通知。
it("渠道列表读取失败时不给出备份表单", async () => {
  render({ listNotifyChannels: async () => { throw new ConnectError("channels unavailable", Code.Unavailable); } });
  await appearanceForm();
  expect(await screen.findByText(/channels unavailable/)).toBeInTheDocument();
  expect(screen.queryByRole("form", { name: "备份到 S3" })).toBeNull();
});
