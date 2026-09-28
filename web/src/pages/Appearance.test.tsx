import { isFieldSet, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SettingsSchema, type UpdateSettingsRequest } from "../gen/probe/v1/admin_pb";
import { MAX_LOGO_BYTES } from "../lib/appearance";
import { BUILT_IN_ACCENT } from "../lib/palette";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Appearance } from "./Appearance";

const current = { title: "机房", theme: "dark", accentColor: "#123abc", logo: "", customCss: "body { margin: 0 }" };
const routes = [{ path: "/appearance", Component: Appearance }];
const render = (impl: AdminImpl) => renderWithAdmin({ getSettings: async () => ({ settings: current }), ...impl }, routes, "/appearance");

async function form() {
  return within(await screen.findByRole("form", { name: "公开页外观" }));
}

// 有状态的 hub 替身，与 store 的 SaveSettings 同语义：外观整体替换（标题去首尾空白，代表 hub 的清洗），总闸与国家查询
// 两项缺席即不变，回显保存后的全部设置。set 模拟别处（另一个面板、脚本）改了设置；holdReads 让之后的 GetSettings 挂起
// 到 releaseReads；failReads 让之后的 GetSettings 一直失败（hub 重启、网络中断），保存后的刷新因此拿不到新值。
type SettingsInit = MessageInitShape<typeof SettingsSchema>;

function statefulHub(initial: SettingsInit) {
  let state: SettingsInit = initial;
  const sent: UpdateSettingsRequest[] = [];
  let held: Promise<void> | null = null;
  let release = () => {};
  let failing = false;
  const impl: AdminImpl = {
    getSettings: async () => {
      if (failing) throw new ConnectError("hub restarting", Code.Unavailable);
      if (held) await held;
      return { settings: state };
    },
    updateSettings: async (req) => {
      sent.push(req);
      const s = req.settings!;
      state = {
        title: s.title.trim(), theme: s.theme, accentColor: s.accentColor, logo: s.logo, customCss: s.customCss,
        publicEnabled: s.publicEnabled ?? state.publicEnabled, geoEnabled: s.geoEnabled ?? state.geoEnabled, geoUrl: s.geoUrl ?? state.geoUrl,
      };
      return { settings: state };
    },
  };
  return {
    impl, sent, state: () => state,
    set: (patch: SettingsInit) => { state = { ...state, ...patch }; },
    holdReads: () => { held = new Promise((r) => { release = () => { held = null; r(); }; }); },
    releaseReads: () => release(),
    failReads: () => { failing = true; },
  };
}

it("公开页总闸显示当前值并显式提交 false 与 true", async () => {
  const hub = statefulHub({ ...current, publicEnabled: true });
  const sent = hub.sent;
  render(hub.impl);
  const f = await form();
  const toggle = f.getByRole("checkbox", { name: "启用公开页" });
  expect(toggle).toBeChecked();
  expect(f.getByText(/节点的公开标记保留/)).toBeInTheDocument();
  fireEvent.click(toggle);
  hub.holdReads();
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.map((r) => r.settings?.publicEnabled)).toEqual([false]));
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  // 保存后的重新拉取还挂着，开关此时显示的只能是写进缓存的回显。
  await waitFor(() => expect(toggle).not.toBeChecked());
  hub.releaseReads();
  await waitFor(() => expect(toggle).toBeEnabled());
  fireEvent.click(toggle);
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.map((r) => r.settings?.publicEnabled)).toEqual([false, true]));
});

// 草稿是开始编辑时的快照。之后别处关了公开页，只改标题的保存不得带总闸——带上快照里的"开"就会把它重新打开。
it("开关没动过时保存不带总闸，别处关掉的公开页不被重新打开", async () => {
  const hub = statefulHub({ ...current, publicEnabled: true });
  const { queryClient } = render(hub.impl);
  const f = await form();
  const toggle = f.getByRole("checkbox", { name: "启用公开页" });
  expect(toggle).toBeChecked();
  fireEvent.change(f.getByLabelText("标题"), { target: { value: "新标题" } });
  hub.set({ publicEnabled: false });
  await act(() => queryClient.refetchQueries());
  // 查询通知经 setTimeout 调度，重新拉取落地后还要等一次渲染。
  await waitFor(() => expect(toggle).not.toBeChecked());
  expect(f.getByLabelText("标题")).toHaveValue("新标题");
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(hub.sent).toHaveLength(1));
  expect(hub.sent[0].settings?.title).toBe("新标题");
  expect(isFieldSet(hub.sent[0].settings!, SettingsSchema.field.publicEnabled)).toBe(false);
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  expect(hub.state().publicEnabled).toBe(false);
  expect(toggle).not.toBeChecked();
  // 动过开关才带：这次显式打开。
  fireEvent.click(toggle);
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(hub.sent).toHaveLength(2));
  expect(isFieldSet(hub.sent[1].settings!, SettingsSchema.field.publicEnabled)).toBe(true);
  expect(hub.sent[1].settings?.publicEnabled).toBe(true);
});

it("表单显示当前设置，标题留空时提示内置标题", async () => {
  render({ getSettings: async () => ({ settings: { ...current, title: "" } }) });
  const f = await form();
  expect(f.getByLabelText("标题")).toHaveValue("");
  expect(f.getByLabelText("标题")).toHaveAttribute("placeholder", "服务器状态");
  expect(f.getByLabelText("明暗")).toHaveValue("dark");
  expect(f.getByLabelText("主色")).toHaveValue("#123abc");
  expect(f.getByLabelText("自定义 CSS")).toHaveValue("body { margin: 0 }");
});

it("保存提交全部外观字段，表单改显 hub 实际保存的值", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: { ...req.settings!, title: "新标题", accentColor: "#abcdef" } }; } });
  const f = await form();
  fireEvent.change(f.getByLabelText("标题"), { target: { value: " 新标题 " } });
  fireEvent.change(f.getByLabelText("明暗"), { target: { value: "auto" } });
  fireEvent.change(f.getByLabelText("主色"), { target: { value: "#ABCDEF" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings).toMatchObject({ title: " 新标题 ", theme: "auto", accentColor: "#ABCDEF", logo: "", customCss: "body { margin: 0 }" });
  expect(f.getByLabelText("标题")).toHaveValue("新标题");
  expect(f.getByLabelText("主色")).toHaveValue("#abcdef");
});

it("hub 拒绝时显示错误原文并保留草稿", async () => {
  render({ updateSettings: async () => { throw new ConnectError('settings.custom_css must not contain "</" (it could end the page\'s <style> element); found at byte 0', Code.InvalidArgument); } });
  const f = await form();
  fireEvent.change(f.getByLabelText("自定义 CSS"), { target: { value: "</style>" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("alert")).toHaveTextContent('settings.custom_css must not contain "</"');
  expect(f.getByLabelText("自定义 CSS")).toHaveValue("</style>");
});

it("选中的 logo 以 data: URL 提交，可移除", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  expect(f.getByLabelText("logo")).toHaveAttribute("accept", "image/png,image/jpeg,image/webp,image/svg+xml");
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" })] } });
  expect(await f.findByRole("img", { name: "logo 预览" })).toHaveAttribute("src", "data:image/png;base64,iVBORw==");
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.map((r) => r.settings?.logo)).toEqual(["data:image/png;base64,iVBORw=="]));
  fireEvent.click(f.getByRole("button", { name: "移除 logo" }));
  expect(f.queryByRole("img", { name: "logo 预览" })).toBeNull();
});

it("超出大小上限的 logo 在提交前报出，不发请求", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  // 98304 字节编码成 131072 个 base64 字符，恰等于上限；加上 data:image/png;base64, 前缀就超出。
  const size = (MAX_LOGO_BYTES / 4) * 3;
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array(size)], "big.png", { type: "image/png" })] } });
  expect(await f.findByRole("alert")).toHaveTextContent(`上限 ${MAX_LOGO_BYTES} 字节`);
  expect(f.getByRole("button", { name: "保存" })).toBeDisabled();
  // 按钮禁用只是提示，不发请求由 submit 里的检查承载：直接触发表单的 submit 事件验证它。
  // 随后换成合规的 logo 再保存；超限的那次若也发出了，会排在前面。
  fireEvent.submit(f.getByRole("button", { name: "保存" }).closest("form")!);
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" })] } });
  await f.findByRole("img", { name: "logo 预览" });
  await waitFor(() => expect(f.getByRole("button", { name: "保存" })).toBeEnabled());
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.length).toBeGreaterThan(0));
  expect(sent[0].settings?.logo).toBe("data:image/png;base64,iVBORw==");
});

// 保存进行中整个表单禁用：飞行中的保存返回时，回显覆盖的只能是这次提交自己送出的内容。
it("保存进行中表单禁用，回显落地后恢复可编辑", async () => {
  let resolve: (v: { settings: typeof current }) => void = () => {};
  let calls = 0;
  render({ updateSettings: () => { calls++; return new Promise((r) => { resolve = r; }); } });
  const f = await form();
  fireEvent.change(f.getByLabelText("标题"), { target: { value: "第一次" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(f.getByLabelText("标题")).toBeDisabled());
  for (const label of ["明暗", "主色", "取色", "logo", "自定义 CSS"]) expect(f.getByLabelText(label)).toBeDisabled();
  expect(f.getByRole("button", { name: "保存" })).toBeDisabled();
  // 按钮禁用之外，submit 自己也不在进行中再发：直接触发 submit 事件验证。
  fireEvent.submit(f.getByRole("button", { name: "保存" }).closest("form")!);
  resolve({ settings: { ...current, title: "第一次" } });
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  expect(f.getByLabelText("标题")).toBeEnabled();
  expect(f.getByLabelText("标题")).toHaveValue("第一次");
  expect(calls).toBe(1);
});

// 读 logo 文件是异步的：读取期间不能保存（读完的回调会改草稿），读取期间改的其他字段在读完后仍在。
it("读 logo 期间不能保存，期间的编辑不丢", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" })] } });
  expect(f.getByRole("button", { name: "保存" })).toBeDisabled();
  fireEvent.submit(f.getByRole("button", { name: "保存" }).closest("form")!);
  fireEvent.change(f.getByLabelText("标题"), { target: { value: "读取期间改的" } });
  await f.findByRole("img", { name: "logo 预览" });
  expect(f.getByLabelText("标题")).toHaveValue("读取期间改的");
  await waitFor(() => expect(f.getByRole("button", { name: "保存" })).toBeEnabled());
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0].settings).toMatchObject({ title: "读取期间改的", logo: "data:image/png;base64,iVBORw==" });
});

// 主色为空时取色器显示内置主色；只有选了别的颜色才算设置，原样的兜底色（浏览器关上取色器时可能再报一次）不提交。
it("取色器的兜底色不当成设置提交", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({
    getSettings: async () => ({ settings: { ...current, accentColor: "" } }),
    updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; },
  });
  const f = await form();
  expect(f.getByLabelText("取色")).toHaveValue(BUILT_IN_ACCENT);
  fireEvent.change(f.getByLabelText("取色"), { target: { value: BUILT_IN_ACCENT } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0].settings?.accentColor).toBe("");
  fireEvent.change(f.getByLabelText("取色"), { target: { value: "#00ff00" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1].settings?.accentColor).toBe("#00ff00");
});

// 可控的 FileReader：读取在测试调用 finish 时才结束，读取进行中的状态可以逐步观察。
class FakeReader {
  static all: FakeReader[] = [];
  result: string | null = null;
  error: unknown = null;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  readAsDataURL() { FakeReader.all.push(this); }
  finish(dataUrl: string) { act(() => { this.result = dataUrl; this.onload?.(); }); }
}

function useFakeReader() {
  FakeReader.all = [];
  vi.stubGlobal("FileReader", FakeReader);
}

afterEach(() => vi.unstubAllGlobals());

const pick = (f: ReturnType<typeof within>, name: string) =>
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([1])], name, { type: "image/png" })] } });

// 至多一个读者在飞：读取进行中文件输入禁用，reading 才恰好等于"有读者在飞"，保存也随之被拒。
// 文件输入若仍可用，第二次选文件会让先读完的那个把 reading 置回 false，保存在另一份还在读时发出。
it("读 logo 进行中文件输入与保存都禁用，读完恢复", async () => {
  useFakeReader();
  render({ updateSettings: async (req) => ({ settings: req.settings }) });
  const f = await form();
  pick(f, "a.png");
  expect(FakeReader.all).toHaveLength(1);
  expect(f.getByLabelText("logo")).toBeDisabled();
  expect(f.getByRole("button", { name: "保存" })).toBeDisabled();
  FakeReader.all[0].finish("data:image/png;base64,QQ==");
  expect(f.getByLabelText("logo")).toBeEnabled();
  expect(f.getByRole("button", { name: "保存" })).toBeEnabled();
  expect(f.getByRole("img", { name: "logo 预览" })).toHaveAttribute("src", "data:image/png;base64,QQ==");
});

it("读完 a 再选 b，最后选的生效", async () => {
  useFakeReader();
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  pick(f, "a.png");
  FakeReader.all[0].finish("data:image/png;base64,QQ==");
  pick(f, "b.png");
  FakeReader.all[1].finish("data:image/png;base64,Qg==");
  expect(f.getByRole("img", { name: "logo 预览" })).toHaveAttribute("src", "data:image/png;base64,Qg==");
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  expect(sent.map((r) => r.settings?.logo)).toEqual(["data:image/png;base64,Qg=="]);
});

// logo 字段的第二个写者"移除 logo"在读取中禁用：否则读取中点了移除，读完的回调又把 logo 写回来。
it("读 logo 进行中移除按钮禁用，读完恢复且移除生效", async () => {
  useFakeReader();
  render({ updateSettings: async (req) => ({ settings: req.settings }) });
  const f = await form();
  pick(f, "a.png");
  FakeReader.all[0].finish("data:image/png;base64,QQ==");
  expect(f.getByRole("button", { name: "移除 logo" })).toBeEnabled();
  pick(f, "b.png");
  expect(f.getByRole("button", { name: "移除 logo" })).toBeDisabled();
  FakeReader.all[1].finish("data:image/png;base64,Qg==");
  expect(f.getByRole("button", { name: "移除 logo" })).toBeEnabled();
  fireEvent.click(f.getByRole("button", { name: "移除 logo" }));
  expect(f.queryByRole("img", { name: "logo 预览" })).toBeNull();
});

// 换 logo 与改其他字段一样：上一次保存的错误不再描述当前草稿，读完即清掉。
it("上次保存失败后换 logo，旧的错误清掉", async () => {
  useFakeReader();
  render({ updateSettings: async () => { throw new ConnectError("settings.logo must be empty or data:<type>;base64,<data>", Code.InvalidArgument); } });
  const f = await form();
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("alert")).toHaveTextContent("settings.logo must be empty");
  pick(f, "a.png");
  FakeReader.all[0].finish("data:image/png;base64,QQ==");
  expect(f.queryByRole("alert")).toBeNull();
});

describe("国家 / 地区查询", () => {
  const geoForm = async () => within(await screen.findByRole("form", { name: "国家 / 地区查询" }));
  // hub 的 GetSettings 总带总闸与查询两项。
  const withGeo = { ...current, publicEnabled: true, geoEnabled: false, geoUrl: "https://ipinfo.io/{ip}/country" };

  it("开关文案写明开启即把节点地址发给哪个服务，随输入的服务地址更新", async () => {
    render({ getSettings: async () => ({ settings: withGeo }) });
    const f = await geoForm();
    const toggle = f.getByRole("checkbox", { name: "按来源地址查询节点的国家 / 地区" });
    expect(toggle).not.toBeChecked();
    expect(f.getByLabelText("服务地址")).toHaveValue("https://ipinfo.io/{ip}/country");
    expect(f.getByLabelText("服务地址")).toHaveAccessibleDescription(/^开启即由 hub 把每个节点的来源地址发给 https:\/\/ipinfo\.io\/\{ip\}\/country（\{ip\} 处换成地址）/);
    fireEvent.change(f.getByLabelText("服务地址"), { target: { value: "https://geo.example/{ip}" } });
    expect(f.getByLabelText("服务地址")).toHaveAccessibleDescription(/^开启即由 hub 把每个节点的来源地址发给 https:\/\/geo\.example\/\{ip\}（/);
  });

  // 文案写出"不再外呼"的上界，不许诺无条件的"每地址一次"：hub 只记住每个节点最近 4 个地址的答案。
  it("开关说明写出答案表的上界", async () => {
    render({ getSettings: async () => ({ settings: withGeo }) });
    const f = await geoForm();
    const description = f.getByLabelText("服务地址");
    expect(description).toHaveAccessibleDescription(/节点停在同一地址时查得一次即止；hub 记住每个节点最近 4 个地址的答案，在这些地址之间切换不再外呼，\s*超过 4 个地址轮换或 hub 重启后会再查。/);
  });

  // 外观表单里未保存的总闸同样不随查询表单提交：查询表单不带总闸，hub 对缺席的总闸不改。外观表单的开关仍显示用户
  // 动过的值，不被查询表单保存后写进缓存的回显（总闸仍开）盖掉。
  it("保存提交开关与服务地址，外观取 hub 的已保存值而不是外观表单的草稿，也不带总闸", async () => {
    const hub = statefulHub(withGeo);
    const sent = hub.sent;
    render(hub.impl);
    const appearance = await form();
    fireEvent.change(appearance.getByLabelText("标题"), { target: { value: "未保存的标题" } });
    fireEvent.click(appearance.getByRole("checkbox", { name: "启用公开页" }));
    const f = await geoForm();
    fireEvent.click(f.getByRole("checkbox", { name: "按来源地址查询节点的国家 / 地区" }));
    fireEvent.click(f.getByRole("button", { name: "保存" }));
    expect(await f.findByRole("status")).toHaveTextContent("已保存");
    expect(sent).toHaveLength(1);
    expect(sent[0].settings).toMatchObject({ ...current, geoEnabled: true, geoUrl: "https://ipinfo.io/{ip}/country" });
    expect(isFieldSet(sent[0].settings!, SettingsSchema.field.publicEnabled)).toBe(false);
    expect(appearance.getByLabelText("标题")).toHaveValue("未保存的标题");
    expect(appearance.getByRole("checkbox", { name: "启用公开页" })).not.toBeChecked();
  });

  it("外观保存后刷新失败，查询表单提交的外观仍是刚保存的回显", async () => {
    const hub = statefulHub(withGeo);
    render(hub.impl);
    const appearance = await form();
    hub.failReads();
    fireEvent.change(appearance.getByLabelText("标题"), { target: { value: " 新标题 " } });
    fireEvent.click(appearance.getByRole("button", { name: "保存" }));
    expect(await appearance.findByRole("status")).toHaveTextContent("已保存");
    expect(await screen.findByText("hub restarting")).toBeInTheDocument();
    const f = await geoForm();
    fireEvent.click(f.getByRole("checkbox", { name: "按来源地址查询节点的国家 / 地区" }));
    fireEvent.click(f.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(hub.sent).toHaveLength(2));
    expect(hub.sent[1].settings).toMatchObject({ ...current, title: "新标题", geoEnabled: true });
  });

  it("查询表单保存后刷新失败，重新进入页面时显示刚保存的开关", async () => {
    const hub = statefulHub(withGeo);
    const { router } = renderWithAdmin(hub.impl, [...routes, { path: "/elsewhere", Component: () => null }], "/appearance");
    const f = await geoForm();
    hub.failReads();
    fireEvent.click(f.getByRole("checkbox", { name: "按来源地址查询节点的国家 / 地区" }));
    fireEvent.click(f.getByRole("button", { name: "保存" }));
    expect(await f.findByRole("status")).toHaveTextContent("已保存");
    expect(await screen.findByText("hub restarting")).toBeInTheDocument();
    await act(() => router.navigate("/elsewhere"));
    await act(() => router.navigate("/appearance"));
    expect((await geoForm()).getByRole("checkbox", { name: "按来源地址查询节点的国家 / 地区" })).toBeChecked();
  });

  it("外观表单不提交查询设置：hub 对缺席的两项不改", async () => {
    const sent: UpdateSettingsRequest[] = [];
    render({ getSettings: async () => ({ settings: { ...withGeo, geoEnabled: true } }), updateSettings: async (req) => { sent.push(req); return { settings: { ...req.settings!, geoEnabled: true, geoUrl: withGeo.geoUrl } }; } });
    const f = await form();
    fireEvent.click(f.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].settings?.geoEnabled).toBeUndefined();
    expect(sent[0].settings?.geoUrl).toBeUndefined();
  });
});
