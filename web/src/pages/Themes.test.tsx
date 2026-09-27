import { afterEach, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ListThemesResponseSchema } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Themes } from "./Themes";

const origin = "https://status.example.com";
const listed = (publicDir = false) => create(ListThemesResponseSchema, {
  themeOrigin: origin, publicDir,
  themes: [
    { id: "dark", name: "Dark", version: "1.2", uploadedAt: 1_700_000_000n, enabled: true, hasPreview: true },
    { id: "plain", name: "Plain", version: "0.1", uploadedAt: 1_700_000_000n },
  ],
});
const routes = [{ path: "/themes", Component: Themes }];
const render = (impl: AdminImpl) => renderWithAdmin({
  listThemes: async () => listed(),
  getThemePreview: async () => ({ content: new Uint8Array([137, 80, 78, 71]), contentType: "image/png" }),
  ...impl,
}, routes, "/themes");

afterEach(() => vi.restoreAllMocks());

const pick = (bytes: number[], name = "theme.zip") => {
  const form = screen.getByRole("form", { name: "上传主题" });
  fireEvent.change(within(form).getByLabelText(/主题包/), { target: { files: [new File([new Uint8Array(bytes)], name, { type: "application/zip" })] } });
  return form;
};

it("未配置主题 origin 时给出说明而不是错误横幅", async () => {
  render({ listThemes: async () => { throw new ConnectError("themes are disabled because this hub has no theme origin", Code.FailedPrecondition); } });
  const note = await screen.findByRole("note", { name: "主题未开启" });
  expect(note).toHaveTextContent("--theme-origin https://status.example.com");
  expect(note).toHaveTextContent("只换端口不算另一个主机名");
  expect(note).toHaveTextContent("themes are disabled because this hub has no theme origin");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("form", { name: "上传主题" })).toBeNull();
});

it("其他错误照常显示为错误横幅", async () => {
  render({ listThemes: async () => { throw new ConnectError("listing themes failed", Code.Internal); } });
  expect(await screen.findByRole("alert")).toHaveTextContent("listing themes failed");
  expect(screen.queryByRole("note", { name: "主题未开启" })).toBeNull();
});

it("列表显示状态、主题 origin 与启用中的主题；没有 --public-dir 时不提示接管", async () => {
  render({});
  const dark = (await screen.findByRole("cell", { name: "Dark" })).closest("tr")!;
  expect(within(dark).getByText("启用中")).toBeInTheDocument();
  expect(within(screen.getByRole("cell", { name: "Plain" }).closest("tr")!).getByText("未启用")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: origin })).toHaveAttribute("href", origin);
  expect(screen.getByText(/那里现在是主题 Dark（dark）/)).toBeInTheDocument();
  expect(screen.queryByText(/--public-dir/)).toBeNull();
});

it("没有启用的主题时说明主题 origin 是内置公开页", async () => {
  render({ listThemes: async () => create(ListThemesResponseSchema, { themeOrigin: origin, themes: [] }) });
  expect(await screen.findByText(/现在没有启用的主题，那里是内置公开页/)).toBeInTheDocument();
  expect(screen.getByText("还没有主题。")).toBeInTheDocument();
});

it("给了 --public-dir 时标明主 origin 被目录接管", async () => {
  render({ listThemes: async () => listed(true) });
  const note = await screen.findByRole("note");
  expect(note).toHaveTextContent("由 --public-dir 的目录接管");
  expect(note).toHaveTextContent(`启用主题只改变 ${origin} 上的页面`);
});

it("有预览图的显示 data: 图片，没有的不请求", async () => {
  const asked: string[] = [];
  render({ getThemePreview: async (req) => { asked.push(req.id); return { content: new Uint8Array([1, 2, 3]), contentType: "image/webp" }; } });
  const img = await screen.findByRole("img", { name: "Dark 预览图" });
  expect(img).toHaveAttribute("src", "data:image/webp;base64,AQID");
  expect(within(screen.getByRole("cell", { name: "Plain" }).closest("tr")!).getByText("无")).toBeInTheDocument();
  expect(asked).toEqual(["dark"]);
});

it("上传把文件原样作为 package 发出，成功后刷新列表与预览", async () => {
  const sent: { pkg: number[]; expect: string }[] = [];
  let lists = 0, previews = 0;
  render({
    listThemes: async () => { lists++; return listed(); },
    getThemePreview: async () => { previews++; return { content: new Uint8Array([1]), contentType: "image/png" }; },
    uploadTheme: async (req) => { sent.push({ pkg: [...req.package], expect: req.expectId }); return { theme: { id: "dark", name: "Dark", version: "1.3" } }; },
  });
  await screen.findByRole("img", { name: "Dark 预览图" });
  const form = pick([80, 75, 3, 4, 9]);
  fireEvent.click(within(form).getByRole("button", { name: "上传" }));
  expect(await screen.findByRole("status")).toHaveTextContent("已上传 Dark（dark）版本 1.3");
  expect(sent).toEqual([{ pkg: [80, 75, 3, 4, 9], expect: "" }]);
  await waitFor(() => expect(lists).toBe(2));
  await waitFor(() => expect(previews).toBe(2));
  expect(within(form).getByRole("button", { name: "上传" })).toBeDisabled();
});

it("选了更新某个主题时带上 expect_id", async () => {
  const expects: string[] = [];
  render({ uploadTheme: async (req) => { expects.push(req.expectId); return { theme: { id: "plain", name: "Plain", version: "0.2" } }; } });
  await screen.findByRole("cell", { name: "Plain" });
  const form = pick([1]);
  fireEvent.change(within(form).getByLabelText("用途"), { target: { value: "plain" } });
  fireEvent.click(within(form).getByRole("button", { name: "上传" }));
  await waitFor(() => expect(expects).toEqual(["plain"]));
});

it("上传失败显示 hub 的错误原文", async () => {
  render({ uploadTheme: async () => { throw new ConnectError(`entry "../x": path must stay inside the package`, Code.InvalidArgument); } });
  await screen.findByRole("cell", { name: "Dark" });
  const form = pick([1]);
  fireEvent.click(within(form).getByRole("button", { name: "上传" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(`entry "../x": path must stay inside the package`);
});

it("说明上传的 30 秒时限", async () => {
  render({});
  expect(await screen.findByText(/30 秒内传完/)).toBeInTheDocument();
});

it("启用按 id，停用发空 id", async () => {
  const ids: string[] = [];
  render({ enableTheme: async (req) => { ids.push(req.id); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "启用 Plain（plain）" }));
  await waitFor(() => expect(ids).toEqual(["plain"]));
  fireEvent.click(screen.getByRole("button", { name: "停用 Dark（dark）" }));
  await waitFor(() => expect(ids).toEqual(["plain", ""]));
});

it("删除需要确认；删启用中的主题时说明回落内置公开页", async () => {
  const deleted: string[] = [];
  render({ deleteTheme: async (req) => { deleted.push(req.id); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "删除 Dark（dark）" }));
  expect(deleted).toEqual([]);
  expect(screen.getByText(`删除后 ${origin} 回落内置公开页`)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "确认删除 Dark（dark）" }));
  await waitFor(() => expect(deleted).toEqual(["dark"]));
});
