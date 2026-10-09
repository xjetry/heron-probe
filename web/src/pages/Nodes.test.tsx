import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Nodes } from "./Nodes";
import { AdminService, CountrySource, GetSnapshotResponseSchema, GetRegisterWindowResponseSchema, type ListNodesRequest } from "../gen/heron/v1/admin_pb";
import { sameTag } from "../lib/tags";
import { withId as withIdLabel } from "../lib/ids";
import { AddressDetectionState, BillingCycle } from "../gen/heron/v1/types_pb";
import { fillSegments, segmentsValue } from "../test/fields";
import { expectEmptyState } from "../test/empty";
import { objectContaining } from "../test/matchers";
import { chooseOption } from "../test/select";

// 行菜单入口携带 id，同名节点仍能定位到各自的操作。
const rowAction = (label: string, action: string) => {
  const trigger = screen.getByRole("button", { name: `更多操作 ${label}` });
  if (trigger.getAttribute("aria-expanded") !== "true") { fireEvent.pointerDown(trigger); fireEvent.click(trigger); }
  return screen.getByRole("menuitem", { name: `${action} ${label}` });
};
const openRowAction = (label: string, action: string) => fireEvent.click(rowAction(label, action));

const filterBox = (name: string) => {
  const trigger = screen.getByRole("button", { name: /^标签/ });
  if (trigger.getAttribute("aria-expanded") !== "true") fireEvent.click(trigger);
  return screen.getByRole("checkbox", { name });
};
const findFilterBox = async (name: string) => {
  const trigger = screen.getByRole("button", { name: /^标签/ });
  if (trigger.getAttribute("aria-expanded") !== "true") fireEvent.click(trigger);
  return screen.findByRole("checkbox", { name });
};
const two = [
  { id: 1n, name: "a", public: false, note: "", sortOrder: 0, createdAt: 0n, trafficResetDay: 1, offlineGraceS: 90 },
  { id: 2n, name: "b", public: true, note: "db", sortOrder: 1, createdAt: 0n, trafficResetDay: 1 },
];

const withVersion = [
  { ...two[0], facts: { hostname: "a", os: "", kernel: "", arch: "", virtualization: "", cpuModel: "", cpuCores: 0, agentVersion: "v1.0.0", icmpAvailable: true } },
];
const agentAt = (agentVersion: string) => [{ ...withVersion[0], facts: { ...withVersion[0].facts, agentVersion } }];
// 按表头文字取列号：同一行里可能有多个"—"，断言要落在指定的列上。
const column = (header: string) => screen.getAllByRole("columnheader").findIndex((th) => th.textContent === header);
const snapshotOf = (hubVersion: string, boundAgentVersion = "") => async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion, boundAgentVersion });
// 节点页总会取快照（落后标记用）与标签清单（过滤器与标签管理用）；默认给一个成功的快照与空清单，需要别的版本、标签或
// 失败的用例覆盖 getSnapshot、listTags。
const renderNodes = (impl: AdminImpl, routes: Parameters<typeof renderWithAdmin>[1] = [{ path: "/nodes", Component: Nodes }]) =>
  renderWithAdmin({ getSnapshot: snapshotOf("v1.1.0"), listTags: async () => ({ tags: [] }), ...impl }, routes, "/nodes");

describe("Nodes", () => {

  it("列表显示 hub 计费用量与配额，不重新求收发之和", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }), getSnapshot: async () => ({ nodes: [{ id: 1n, name: "a", traffic: { periodRx: 800n, periodTx: 200n, quotaUsedBytes: 200n, quotaBytes: 1000n, quotaUsedPct: 20 } }] }) });
    const amount = await screen.findByText("200 B / 1000 B");
    expect(amount).toHaveClass("cell-main");
    expect(amount.closest("td")).toHaveTextContent(/^200 B \/ 1000 B 20\.0%$/);
  });

  it("双栈结果区分地址、不支持、失败与未上报，并在详情显示探测时间", async () => {
    renderNodes({ listNodes: async () => ({ nodes: [
      { ...two[0], facts: { network: { ipv4: { state: AddressDetectionState.AVAILABLE, address: "8.8.8.8", checkedAt: 1790679000n }, ipv6: { state: AddressDetectionState.UNSUPPORTED, checkedAt: 1790679000n } } } },
      { ...two[1], facts: { network: { ipv4: { state: AddressDetectionState.FAILED, checkedAt: 1790679000n } } } },
    ] }) });
    const a = within((await screen.findByRole("link", { name: "a（#1）" })).closest("tr")!);
    expect(a.getByLabelText("IPv4")).toHaveTextContent("8.8.8.8");
    expect(a.getByLabelText("IPv6")).toHaveTextContent("不支持");
    const b = within(screen.getByRole("link", { name: "b（#2）" }).closest("tr")!);
    expect(b.getByLabelText("IPv4")).toHaveTextContent("探测失败");
    expect(b.getByLabelText("IPv6")).toHaveTextContent("等待上报");
    openRowAction("a（#1）", "编辑");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("IPv4").querySelector("time")).toHaveAttribute("datetime", "2026-09-29T10:50:00.000Z");
  });

  it("打开单一弹窗后不能切换节点，取消与 Escape 关闭时不提交", async () => {
    const updateNode = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(rowAction("b（#2）", "编辑")).toHaveAttribute("aria-disabled", "true");
    openRowAction("b（#2）", "编辑");
    expect(screen.getByRole("dialog")).toHaveAccessibleName("编辑节点 · a（#1）");
    fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "discarded" } });
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { bubbles: true, cancelable: true }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(updateNode).not.toHaveBeenCalled();
    openRowAction("a（#1）", "编辑");
    expect(screen.getByLabelText("名称 a（#1）")).toHaveValue("a");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("费用分组保存保留基础信息、标签、国家与宽限期", async () => {
    const updateNode = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: [{ ...two[0], note: "保留", countryPin: "JP", tags: ["prod"] }] }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    expect(screen.getByRole("dialog")).toHaveAccessibleName("编辑节点 · a（#1）");
    expect(screen.getByLabelText("名称 a（#1）")).toHaveValue("a");
    fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("radio", { name: "USD" }));
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, name: "a", public: false, note: "保留", countryPin: "JP", tags: ["prod"], offlineGraceS: 90, trafficResetDay: 1, billing: objectContaining({ price: "5", currency: "USD" }) }), expect.anything()));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("来源 IP 在编辑诊断中保留，未记录时明确提示", async () => {
    renderNodes({ listNodes: async () => ({ nodes: [{ ...two[0], lastSource: "203.0.113.7" }, two[1]] }) });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.getByRole("columnheader", { name: "IPv4 / IPv6" })).toBeInTheDocument();
    openRowAction("a（#1）", "编辑");
    expect(within(screen.getByRole("dialog")).getByText("203.0.113.7")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    openRowAction("b（#2）", "编辑");
    expect(within(screen.getByRole("dialog")).getByText("尚未记录来源")).toBeInTheDocument();
  });

  it.each(["ALPHA", "CUSTOMER", "HOSTNAME"])("搜索 %s 后只显示命中节点，清空恢复全部", async (search) => {
    const matching = { ...two[0], name: "alpha", note: "customer", facts: { hostname: "hostname.internal" } };
    renderNodes({ listNodes: async () => ({ nodes: [matching, two[1]] }) });
    await screen.findByRole("link", { name: "b（#2）" });
    const input = screen.getByRole("searchbox", { name: "搜索节点" });
    fireEvent.change(input, { target: { value: search } });
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["alpha"]);
    fireEvent.change(input, { target: { value: "absent" } });
    expect(screen.queryAllByRole("link")).toEqual([]);
    await expectEmptyState("没有匹配的节点。", { region: "节点管理", status: true });
    fireEvent.change(input, { target: { value: "" } });
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["alpha", "b"]);
  });

  it("搜索中禁用全部排序入口，清空后可提交完整排列", async () => {
    const reorderNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: two }), reorderNodes });
    await screen.findByRole("link", { name: "b（#2）" });
    const input = screen.getByRole("searchbox", { name: "搜索节点" });
    fireEvent.change(input, { target: { value: "b" } });
    expect(screen.getByText("筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。")).toBeInTheDocument();
    for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) {
      expect(button).toBeDisabled();
      fireEvent.click(button);
    }
    for (const action of ["上移一位", "下移一位", "置顶", "置底"]) expect(rowAction("b（#2）", action)).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(rowAction("b（#2）", "上移一位"));
    await act(async () => {});
    expect(reorderNodes).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: "" } });
    expect(screen.queryByText("筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。")).toBeNull();
    for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) expect(button).toBeEnabled();
    openRowAction("a（#1）", "下移一位");
    await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 1n] }), expect.anything()));
  });

  it("marks nodes against the bound agent version, not the hub version", async () => {
    // hub v0.5.6 只改了 hub，绑定 agent v0.5.4：跑 v0.5.4 的节点不落后，跑 v0.5.3 的才落后。
    const nodes = [{ ...agentAt("v0.5.4")[0], id: 1n, name: "current" }, { ...agentAt("v0.5.3")[0], id: 2n, name: "behind" }];
    renderNodes({ listNodes: async () => ({ nodes }), getSnapshot: snapshotOf("v0.5.6", "v0.5.4") });
    expect(await screen.findByText("agent 低于 v0.5.4")).toBeInTheDocument();
    expect(screen.getAllByText("agent 低于 v0.5.4")).toHaveLength(1);
  });

  it.each(["", "dev"])("no lagging marker when the hub has no stable binding ('%s')", async (bound) => {
    renderNodes({ listNodes: async () => ({ nodes: agentAt("v0.5.3") }), getSnapshot: snapshotOf("v0.5.6", bound) });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.queryByText(/agent 低于/)).toBeNull();
  });

  it("marks a node whose agent version lags the bound version", async () => {
    renderNodes({
      listNodes: async () => ({ nodes: withVersion }),
      getSnapshot: snapshotOf("v1.1.0", "v1.1.0"),
    });
    expect(await screen.findByText("agent 低于 v1.1.0")).toBeInTheDocument();
    // 标记在链接之外，不改变链接的可访问名。
    expect(screen.getByRole("link", { name: "a（#1）" })).not.toHaveTextContent("agent 低于");
  });

  it("does not mark a node already at the bound version", async () => {
    renderNodes({
      listNodes: async () => ({ nodes: agentAt("v1.1.0") }),
      getSnapshot: snapshotOf("v1.1.0", "v1.1.0"),
    });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.queryByText(/agent 低于/)).toBeNull();
  });

  it("does not mark a node newer than the bound version", async () => {
    renderNodes({
      listNodes: async () => ({ nodes: agentAt("v1.2.0") }),
      getSnapshot: snapshotOf("v1.1.0", "v1.1.0"),
    });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.queryByText(/agent 低于/)).toBeNull();
  });

  it("does not mark a dev agent", async () => {
    renderNodes({
      listNodes: async () => ({ nodes: agentAt("dev") }),
      getSnapshot: snapshotOf("v1.1.0", "v1.1.0"),
    });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.queryByText(/agent 低于/)).toBeNull();
  });

  it.each([["v1.9.0", "v1.10.0"], ["v1.1.0-rc.1", "v1.1.1"], ["v0.9.9", "v1.0.0"], ["v1.1.0-rc.1", "v1.1.0"]])("按版本号比较：%s 落后于 %s", async (agent, bound) => {
    renderNodes({
      listNodes: async () => ({ nodes: agentAt(agent) }),
      getSnapshot: snapshotOf("v1.1.0", bound),
    });
    expect(await screen.findByText(`agent 低于 ${bound}`)).toBeInTheDocument();
  });

  it("快照失败显示版本比较不可用的横幅，列表仍在且不标记", async () => {
    renderNodes({
      listNodes: async () => ({ nodes: withVersion }),
      getSnapshot: async () => { throw new ConnectError("snapshot unavailable", Code.Unavailable); },
    });
    expect(await screen.findByRole("alert")).toHaveTextContent(/落后标记不可用.*snapshot unavailable/);
    expect(screen.getByRole("link", { name: "a（#1）" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "添加节点" })).toBeInTheDocument();
    expect(screen.queryByText(/agent 低于/)).toBeNull();
  });

  it("快照已取到后刷新失败，标记保留并说明按上次的版本判断", async () => {
    let fail = false;
    const { queryClient } = renderNodes({
      listNodes: async () => ({ nodes: withVersion }),
      getSnapshot: async () => {
        if (fail) throw new ConnectError("snapshot refresh failed", Code.Unavailable);
        return { now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion: "v1.1.0", boundAgentVersion: "v1.1.0" };
      },
    });
    expect(await screen.findByText("agent 低于 v1.1.0")).toBeInTheDocument();
    fail = true;
    const snapshotKey = createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" });
    await act(async () => { await queryClient.refetchQueries({ queryKey: snapshotKey }); });
    expect(await screen.findByRole("alert")).toHaveTextContent(/按上次取得的绑定版本 v1\.1\.0 判断.*snapshot refresh failed/);
    expect(screen.getByText("agent 低于 v1.1.0")).toBeInTheDocument();
  });

  it("节点刷新失败保留编辑行与草稿", async () => {
    let fail = false;
    const { queryClient } = renderNodes({ listNodes: async () => {
      if (fail) throw new ConnectError("nodes refresh failed", Code.Unavailable);
      return { nodes: two };
    } });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    const input = screen.getByLabelText("名称 a（#1）");
    fireEvent.change(input, { target: { value: "尚未保存" } });
    fail = true;
    await act(async () => { await queryClient.refetchQueries(); });
    expect(await screen.findByRole("alert")).toHaveTextContent("nodes refresh failed");
    expect(screen.getByLabelText("名称 a（#1）")).toBe(input);
    expect(input).toHaveValue("尚未保存");
    expect(screen.getByRole("link", { name: "b（#2）" })).toBeInTheDocument();
  });

  // 门控只罩节点表格：创建、搜索与标签过滤都不读节点列表，不等它就绪。
  it("列表挂起时表格处显示加载中，创建、搜索与标签过滤照常显示", async () => {
    renderNodes({ listNodes: () => new Promise(() => {}), listTags: async () => ({ tags: [{ name: "db", nodeCount: 0 }] }) });
    expect(await screen.findByText("加载中…")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "节点管理" })).toBeNull();
    expect(screen.getByRole("searchbox", { name: "搜索节点" })).toBeInTheDocument();
    expect(await findFilterBox("db")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "添加节点" })).toBeInTheDocument();
  });

  it("确认删除在列表刷新完成前保持禁用", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const listNodes = vi.fn(async () => {
      if (listNodes.mock.calls.length > 1) { await gate; return { nodes: [two[1]] }; }
      return { nodes: two };
    });
    renderNodes({ listNodes, deleteNode: async () => ({}) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "删除");
    const button = screen.getByRole("menuitem", { name: "确认删除 a（#1）" });
    vi.useFakeTimers();
    try {
      await act(async () => { fireEvent.click(button); await vi.advanceTimersByTimeAsync(100); });
      expect(listNodes).toHaveBeenCalledTimes(2);
      expect(rowAction("a（#1）", "删除")).toHaveAttribute("aria-disabled", "true");
    } finally { vi.useRealTimers(); await act(async () => { release(); }); }
    await waitFor(() => expect(screen.queryByRole("menuitem", { name: "确认删除 a（#1）" })).toBeNull());
  });

  it("编辑往返撤销已武装的删除确认", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "删除");
    expect(screen.getByRole("menuitem", { name: "确认删除 a（#1）" })).toBeInTheDocument();
    openRowAction("a（#1）", "编辑");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("menuitem", { name: "确认删除 a（#1）" })).toBeNull();
    expect(rowAction("a（#1）", "删除")).toBeInTheDocument();
    expect(rowAction("b（#2）", "删除")).toBeInTheDocument();
  });

  it.each(["rotate", "reorder"])("%s 挂起持续到节点列表刷新完成", async (operation) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const listNodes = vi.fn(async () => {
      if (listNodes.mock.calls.length > 1) await gate;
      return { nodes: two };
    });
    const { queryClient } = renderNodes({
      listNodes, rotateNodeToken: async () => ({ token: "new" }), reorderNodes: async () => ({}),
    });
    await screen.findByRole("link", { name: "a（#1）" });
    // 菜单在启用假定时器之前打开：菜单的定位与焦点走真实的异步时序。
    const action = operation === "rotate" ? "换 token" : "下移一位";
    rowAction("a（#1）", action);
    vi.useFakeTimers();
    try {
      await act(async () => {
        fireEvent.click(screen.getByRole("menuitem", { name: `${action} a（#1）` }));
        await vi.advanceTimersByTimeAsync(100);
      });
      expect(listNodes).toHaveBeenCalledTimes(2);
      if (operation === "rotate") expect(queryClient.isMutating()).toBe(1);
      else expect(screen.getByRole("status")).toHaveTextContent("正在保存并确认排序");
    } finally { vi.useRealTimers(); await act(async () => { release(); }); }
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));
    if (operation === "reorder") await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
  });

  it("保存挂起时禁止切换和关闭，刷新完成才关闭抽屉", async () => {
    let releaseSave!: () => void;
    let releaseList!: () => void;
    const saveGate = new Promise<void>((r) => { releaseSave = r; });
    const listGate = new Promise<void>((r) => { releaseList = r; });
    let current = two;
    const listNodes = vi.fn(async () => { if (listNodes.mock.calls.length > 1) await listGate; return { nodes: current }; });
    const updateNode = vi.fn(async () => { await saveGate; current = [{ ...two[0], name: "changed" }, two[1]]; return {}; });
    renderNodes({ listNodes, updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "changed" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    try {
      await waitFor(() => expect(updateNode).toHaveBeenCalledTimes(1));
      expect(rowAction("b（#2）", "编辑")).toHaveAttribute("aria-disabled", "true");
      expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "关闭抽屉" })).toBeDisabled();
      fireEvent(screen.getByRole("dialog"), new Event("cancel", { bubbles: true, cancelable: true }));
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      vi.useFakeTimers();
      await act(async () => { releaseSave(); await vi.advanceTimersByTimeAsync(100); });
      expect(listNodes).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    } finally { vi.useRealTimers(); await act(async () => { releaseSave(); releaseList(); }); }
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("link", { name: "changed（#1）" })).toBeInTheDocument();
    expect(rowAction("b（#2）", "编辑")).not.toHaveAttribute("aria-disabled", "true");
  });

  it("编辑保存成功但回读失败时保留草稿并说明已经保存", async () => {
    let failRead = false;
    const updateNode = vi.fn(async () => { failRead = true; return {}; });
    renderNodes({ listNodes: async () => {
      if (failRead) throw new ConnectError("readback unavailable", Code.Unavailable);
      return { nodes: two };
    }, updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    const field = screen.getByLabelText("名称 a（#1）");
    const value = "draft";
    fireEvent.change(field, { target: { value } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await screen.findByText(/已保存，但回读失败/);
    expect(field).toHaveValue(value);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存" })).toBeEnabled();
    expect(updateNode).toHaveBeenCalledTimes(1);
  });

  it("最新操作清掉创建旧错误，编辑失败显示自己的正文", async () => {
    let rejectEdit = false;
    renderNodes({ listNodes: async () => ({ nodes: two }),
      createNode: async () => { throw new ConnectError("create rejected", Code.InvalidArgument); },
      updateNode: async () => { if (rejectEdit) throw new ConnectError("edit rejected", Code.InvalidArgument); return {}; },
    });
    await screen.findByRole("link", { name: "a（#1）" });
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/^create rejected$/);
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    openRowAction("a（#1）", "编辑");
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByLabelText("名称 a（#1）")).toBeNull());
    expect(screen.queryByRole("alert")).toBeNull();
    rejectEdit = true;
    openRowAction("a（#1）", "编辑");
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/^edit rejected$/);
  });

  it("编辑保存挂起与失败保留草稿，成功后才退出", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const updateNode = vi.fn(async () => {
      await gate;
      if (updateNode.mock.calls.length === 1) throw new ConnectError("node update rejected", Code.InvalidArgument);
      return { node: { ...two[0], name: "changed" } };
    });
    renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "changed" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    try {
      await waitFor(() => expect(updateNode).toHaveBeenCalledTimes(1));
      expect(screen.getByLabelText("名称 a（#1）")).toHaveValue("changed");
      expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    } finally { await act(async () => { release(); }); }
    expect(await screen.findByRole("alert")).toHaveTextContent(/^node update rejected$/);
    expect(screen.getByLabelText("名称 a（#1）")).toHaveValue("changed");
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByLabelText("名称 a（#1）")).toBeNull());
  });
  it.each(["", "29", "1.5", "15"])("重置日 %s 只有 1–28 的整数能保存", async (value) => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("重置日 a（#1）"), { target: { value } });
    if (value === "15") expect(screen.getByRole("button", { name: "保存" })).toBeEnabled();
    else expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  });

  it("编辑重置日说明周期量清零的后果", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    expect(screen.getByText("若从本周期起点算起新的重置日已经过去，本周期用量会立即清零。")).toBeInTheDocument();
  });

  it("编辑回显重置日", async () => {
    renderNodes({ listNodes: async () => ({ nodes: [{ ...two[0], trafficResetDay: 20 }] }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    expect(screen.getByLabelText("重置日 a（#1）")).toHaveValue(20);
  });

  it("变更只失效节点列表，不失效快照与注册窗口", async () => {
    const listNodes = vi.fn(async () => ({ nodes: two }));
    const { queryClient } = renderNodes({ listNodes, createNode: async () => ({ node: two[0], token: "new" }) });
    const snapshotKey = createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" });
    const windowKey = createConnectQueryKey({ schema: AdminService.method.getRegisterWindow, cardinality: "finite" });
    queryClient.setQueryData(snapshotKey, create(GetSnapshotResponseSchema));
    queryClient.setQueryData(windowKey, create(GetRegisterWindowResponseSchema));
    await screen.findByRole("link", { name: "a（#1）" });
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    await waitFor(() => expect(listNodes).toHaveBeenCalledTimes(2));
    expect([snapshotKey, windowKey].map((key) => queryClient.getQueryState(key)?.isInvalidated)).toEqual([false, false]);
  });

  it("再次编辑从当前节点而非旧草稿开始", async () => {
    let list = two;
    const { queryClient } = renderNodes({ listNodes: async () => ({ nodes: list }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "abandoned" } });
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    list = [{ ...two[0], name: "current", public: true, note: "current note" }, two[1]];
    await act(async () => { await queryClient.refetchQueries(); });
    await screen.findByRole("link", { name: "current（#1）" });
    openRowAction("current（#1）", "编辑");
    expect({
      name: screen.getByLabelText<HTMLInputElement>("名称 current（#1）").value,
      public: screen.getByLabelText<HTMLInputElement>("公开 current（#1）").checked,
      note: screen.getByLabelText<HTMLInputElement>("备注 current（#1）").value,
    }).toEqual({ name: "current", public: true, note: "current note" });
  });

  it.each(["delete", "rotate"])("%s 请求挂起时禁止重复操作", async (operation) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const pending = async () => { await gate; return { token: "new" }; };
    renderNodes({ listNodes: async () => ({ nodes: two }), deleteNode: async () => { await gate; return {}; }, rotateNodeToken: pending });
    await screen.findByRole("link", { name: "a（#1）" });
    if (operation === "delete") openRowAction("a（#1）", "删除");
    const action = operation === "delete" ? "删除" : "换 token";
    const button = operation === "delete" ? screen.getByRole("menuitem", { name: "确认删除 a（#1）" }) : rowAction("a（#1）", action);
    fireEvent.click(button);
    try { await waitFor(() => expect(rowAction("a（#1）", action)).toHaveAttribute("aria-disabled", "true")); }
    finally { await act(async () => { release(); }); }
  });
  it("换发未完成时不能启动另一个弹窗，展示后焦点回到原入口", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const createNode = vi.fn(async () => ({ node: two[1], token: "other" }));
    renderNodes({ listNodes: async () => ({ nodes: two }), createNode, rotateNodeToken: async () => { await gate; return { token: "new-token" }; } });
    const rotate = await screen.findByRole("button", { name: "更多操作 a（#1）" });
    rotate.focus();
    openRowAction("a（#1）", "换 token");
    try {
      await waitFor(() => expect(rowAction("a（#1）", "换 token")).toHaveAttribute("aria-disabled", "true"));
      expect(screen.getByRole("button", { name: "添加节点" })).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
      for (const action of ["编辑", "换 token"]) {
        const button = rowAction("b（#2）", action);
        expect(button).toHaveAttribute("aria-disabled", "true");
        fireEvent.click(button);
      }
      fireEvent.pointerDown(document.body);
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(createNode).not.toHaveBeenCalled();
    } finally { await act(async () => { release(); }); }
    expect(await screen.findByLabelText("节点 a（#1） 的新 token")).toHaveTextContent("new-token");
    expect(screen.getByRole("dialog", { name: "节点凭据" })).toHaveClass("drawer");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "完成" }));
    await waitFor(() => expect(rotate).toHaveFocus());
    expect(screen.getByRole("button", { name: "添加节点" })).toBeEnabled();
  });

  it.each(["list", "create"])("%s 失败时展示错误正文", async (source) => {
    const fail = async () => { throw new ConnectError("request failed", Code.Unavailable); };
    renderNodes({ listNodes: source === "list" ? fail : async () => ({ nodes: two }), createNode: fail });
    if (source === "create") {
      await screen.findByRole("link", { name: "a（#1）" });
      fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
      fireEvent.click(screen.getByRole("button", { name: "创建" }));
    }
    expect(await screen.findByRole("alert")).toHaveTextContent(/^request failed$/);
  });
  it("创建后一次性展示 token", async () => {
    const createNode = vi.fn(async () => ({ node: { ...two[0], id: 3n, name: "c" }, token: "deadbeef" }));
    const { router } = renderNodes({ listNodes: async () => ({ nodes: two }), createNode }, [
      { path: "/nodes", Component: Nodes }, { path: "/away", element: <h1>away</h1> },
    ]);
    await screen.findByRole("link", { name: "a（#1）" });
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    expect(await screen.findByLabelText("节点 c（#3） 的 token")).toHaveTextContent("deadbeef");
    expect(screen.getByRole("dialog", { name: "节点已创建" })).toHaveClass("drawer");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    for (const tool of ["curl", "wget"]) {
      const command = within(screen.getByRole("dialog")).getByLabelText(`${tool} 安装命令`);
      expect(command).toHaveTextContent("--key deadbeef");
      expect(command).not.toHaveTextContent("--re-register");
    }
    expect(createNode).toHaveBeenCalledWith(expect.objectContaining({ name: "c" }), expect.anything());
    await act(() => router.navigate("/away"));
    await act(() => router.navigate("/nodes"));
    await screen.findByRole("link", { name: "a（#1）" });
    expect(screen.queryByText("deadbeef")).not.toBeInTheDocument();
  });

  it("添加节点可选填计费，没填时请求不带 billing；成功后草稿清空", async () => {
    const createNode = vi.fn(async (..._args: unknown[]) => ({ node: { ...two[0], id: 3n, name: "c" }, token: "deadbeef" }));
    renderNodes({ listNodes: async () => ({ nodes: two }), createNode });
    await screen.findByRole("link", { name: "a（#1）" });
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    expect(screen.getByRole("dialog", { name: "添加节点" })).toHaveClass("drawer");
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.change(screen.getByLabelText("价格 新节点"), { target: { value: "12.50" } });
    fireEvent.click(screen.getByRole("radio", { name: "USD" }));
    fireEvent.click(screen.getByRole("radio", { name: "每月" }));
    fillSegments("到期日 新节点", "2027-01-31");
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    await waitFor(() => expect(createNode).toHaveBeenCalledWith(expect.objectContaining({
      name: "c",
      billing: objectContaining({ price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2027-01-31", autoRenew: false }),
    }), expect.anything()));
    await screen.findByLabelText("节点 c（#3） 的 token");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "完成" }));
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    expect(screen.getByLabelText<HTMLInputElement>("价格 新节点").value).toBe("");
    expect(screen.getByRole("radio", { name: "未设置" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "无周期" })).toBeChecked();
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "d" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    await waitFor(() => expect(createNode).toHaveBeenLastCalledWith(expect.objectContaining({ name: "d" }), expect.anything()));
    expect(createNode.mock.lastCall?.[0]).not.toHaveProperty("billing");
  });

  it("删除卡片所属节点时清掉明文，删除别的保留", async () => {
    const nodes = [{ ...two[0], id: 3n, name: "c" }, two[1]];
    renderNodes({
      listNodes: async () => ({ nodes }),
      createNode: async () => ({ node: { id: 3n, name: "c" }, token: "deadbeef" }),
      deleteNode: async () => ({}),
    });
    await screen.findByRole("link", { name: "c（#3）" });
    fireEvent.click(screen.getByRole("button", { name: "添加节点" }));
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    expect(await screen.findByLabelText("节点 c（#3） 的 token")).toBeInTheDocument();
    openRowAction("b（#2）", "删除");
    fireEvent.click(screen.getByRole("menuitem", { name: "确认删除 b（#2）" }));
    expect(await screen.findByLabelText("节点 c（#3） 的 token")).toBeInTheDocument();
    openRowAction("c（#3）", "删除");
    fireEvent.click(screen.getByRole("menuitem", { name: "确认删除 c（#3）" }));
    await waitFor(() => expect(screen.queryByLabelText("节点 c（#3） 的 token")).toBeNull());
  });

  it("删除需要二次确认", async () => {
    const deleteNode = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: two }), deleteNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "删除");
    expect(deleteNode).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("menuitem", { name: "确认删除 a（#1）" }));
    await waitFor(() => expect(deleteNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }), expect.anything()));
  });

  it("同名节点的删除按钮按 id 区分并删除正确行", async () => {
    const sameName = [two[0], { ...two[1], id: 11n, name: "a" }];
    const deleteNode = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: sameName }), deleteNode });
    await screen.findAllByRole("link", { name: /^a（#/ });
    openRowAction("a（#11）", "删除");
    fireEvent.click(screen.getByRole("menuitem", { name: "确认删除 a（#11）" }));
    await waitFor(() => expect(deleteNode).toHaveBeenCalledWith(expect.objectContaining({ id: 11n }), expect.anything()));
  });

  it("同名节点先取消再编辑时按 id 保存正确目标", async () => {
    const sameName = [two[0], { ...two[1], id: 11n, name: "a" }];
    const updateNode = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: sameName }), updateNode });
    await screen.findAllByRole("link", { name: /^a（#/ });
    openRowAction("a（#1）", "编辑");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    openRowAction("a（#11）", "编辑");
    // 带 id 时只命中第二行；名称不含 id 时两行同名，getBy 必须报多个。
    const nameInput = screen.getByLabelText((label) => label === "名称 a（#11）" || label === "名称 a");
    fireEvent.change(nameInput, { target: { value: "renamed" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 11n, name: "renamed" }), expect.anything()));
  });

  it("行菜单的上下移与置顶置底：首行不能上移、置顶，末行不能下移、置底", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "b（#2）" });
    const cases = [["a（#1）", ["上移一位", "置顶"], ["下移一位", "置底"]], ["b（#2）", ["下移一位", "置底"], ["上移一位", "置顶"]]] as const;
    for (const [label, off, on] of cases) {
      for (const action of off) expect(rowAction(label, action)).toHaveAttribute("aria-disabled", "true");
      for (const action of on) expect(rowAction(label, action)).not.toHaveAttribute("aria-disabled");
    }
  });

  it.each([["a（#1）", "down"], ["b（#2）", "up"]])("移动 %s %s 提交完整排列", async (label, value) => {
    const reorderNodes = vi.fn(async () => ({}));
    const three = [...two, { ...two[0], id: 3n, name: "c", sortOrder: 2 }];
    renderNodes({ listNodes: async () => ({ nodes: three }), reorderNodes });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction(label, value === "down" ? "下移一位" : "上移一位");
    await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 1n, 3n] }), expect.anything()));
  });

  it("拖动手柄放到目标节点之后提交插入排列，并展示保存反馈", async () => {
    let rows = [...two, { ...two[0], id: 3n, name: "c", sortOrder: 2 }];
    const reorderNodes = vi.fn(async ({ ids }: { ids: bigint[] }) => {
      rows = ids.map((id) => rows.find((node) => node.id === id)!);
      return {};
    });
    renderNodes({ listNodes: async () => ({ nodes: rows }), reorderNodes });
    const handle = await screen.findByRole("button", { name: "调整顺序 a（#1）" });
    const row = screen.getByRole("link", { name: "c（#3）" }).closest("tr")!;
    const dataTransfer = { setData: vi.fn(), effectAllowed: "", dropEffect: "" };
    fireEvent.dragStart(handle, { dataTransfer });
    fireEvent.dragOver(row, { dataTransfer, clientY: 10 });
    expect(row).toHaveClass("drop-after");
    fireEvent.drop(row, { dataTransfer, clientY: 10 });
    await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 3n, 1n] }), expect.anything()));
    expect(await screen.findByText("顺序已保存")).toBeVisible();
    expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["b", "c", "a"]);
  });

  it("拖动中切换筛选后放下不能提交子集排列，外部拖入也不提交", async () => {
    const reorderNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: async () => ({ nodes: two }), reorderNodes });
    const handle = await screen.findByRole("button", { name: "调整顺序 a（#1）" });
    const row = screen.getByRole("link", { name: "b（#2）" }).closest("tr")!;
    const dataTransfer = { setData: vi.fn(), effectAllowed: "", dropEffect: "" };
    fireEvent.drop(row, { dataTransfer, clientY: 10 });
    fireEvent.dragStart(handle, { dataTransfer });
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索节点" }), { target: { value: "b" } });
    fireEvent.drop(row, { dataTransfer, clientY: 10 });
    expect(reorderNodes).not.toHaveBeenCalled();
  });

  it("编辑回传全部字段，没碰的计费也按当前值回传", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    // UpdateNode 整体替换，billing 缺失等于五项全清：只改名称时提交体里的计费必须是节点当前的五项。
    const billed = { ...two[0], publicRemark: "联通 4837", billing: { price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", daysLeft: 60, autoRenew: true } };
    renderNodes({ listNodes: async () => ({ nodes: [billed, two[1]] }), updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("名称 a（#1）"), { target: { value: "a2" } });
    fireEvent.click(screen.getByLabelText("公开 a（#1）"));
    fireEvent.change(screen.getByLabelText("备注 a（#1）"), { target: { value: "changed note" } });
    fireEvent.change(screen.getByLabelText("公开备注 a（#1）"), { target: { value: "移动 CMI" } });
    fireEvent.change(screen.getByLabelText("重置日 a（#1）"), { target: { value: "15" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
      id: 1n, name: "a2", public: true, note: "changed note", publicRemark: "移动 CMI", trafficResetDay: 15,
      billing: objectContaining({ price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", autoRenew: true }),
    }), expect.anything()));
  });

  describe("国家 / 地区", () => {
    const located = [
      { ...two[0], country: "US", countrySource: CountrySource.LOOKUP, countryLookup: "US", countryIp: "8.8.8.8" },
      { ...two[1], country: "JP", countrySource: CountrySource.MANUAL, countryLookup: "US", countryIp: "8.8.4.4", countryPin: "JP" },
      { ...two[0], id: 3n, name: "c" },
      { ...two[0], id: 4n, name: "d", country: "DE", countrySource: CountrySource.MANUAL, countryPin: "DE" },
    ];
    const row = async (name: string) => within((await screen.findByRole("link", { name })).closest("tr")!);
    const countryCell = async (name: string) => (await row(name)).getByLabelText(`国家 / 地区 ${name}`);

    it("列出显示值的徽章、来源与查得值，手动指定时查得值与它所属的地址照写，没有国家是破折号", async () => {
      renderNodes({ listNodes: async () => ({ nodes: located }) });
      expect(await countryCell("a（#1）")).toHaveTextContent(/^US$/);
      expect(await countryCell("a（#1）")).toHaveAttribute("title", "查得于 8.8.8.8");
      expect(await countryCell("b（#2）")).toHaveTextContent(/^JP$/);
      expect(await countryCell("b（#2）")).toHaveAttribute("title", "手动指定；查得 US（于 8.8.4.4）");
      expect(await countryCell("c（#3）")).toHaveTextContent("地区未知");
      expect(await countryCell("d（#4）")).toHaveAttribute("title", "手动指定");
    });

    it("编辑表单回显手动值并转成大写提交；只改别的字段时手动值按当前值回传；提示查得值与它所属的地址", async () => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: located }), updateNode });
      await screen.findByRole("link", { name: "b（#2）" });
      openRowAction("b（#2）", "编辑");
      const pin = screen.getByLabelText("手动指定国家 / 地区 b（#2）");
      expect(pin).toHaveValue("JP");
      expect(pin).toHaveAccessibleDescription("两个字母（ISO 3166-1），优先于查得值；留空用查得值：查得 US（于 8.8.4.4）。");
      fireEvent.change(screen.getByLabelText("备注 b（#2）"), { target: { value: "moved" } });
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 2n, note: "moved", countryPin: "JP" }), expect.anything()));

      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      openRowAction("a（#1）", "编辑");
      expect(screen.getByLabelText("手动指定国家 / 地区 a（#1）")).toHaveValue("");
      fireEvent.change(screen.getByLabelText("手动指定国家 / 地区 a（#1）"), { target: { value: "de" } });
      expect(screen.getByLabelText("手动指定国家 / 地区 a（#1）")).toHaveValue("DE");
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenLastCalledWith(expect.objectContaining({ id: 1n, countryPin: "DE" }), expect.anything()));
    });

    it("尚无查得值时提示", async () => {
      renderNodes({ listNodes: async () => ({ nodes: located }) });
      await screen.findByRole("link", { name: "c（#3）" });
      openRowAction("c（#3）", "编辑");
      expect(screen.getByLabelText("手动指定国家 / 地区 c（#3）")).toHaveAccessibleDescription("两个字母（ISO 3166-1），优先于查得值；留空用查得值：尚无查得值。");
    });
  });

  describe("计费", () => {
    // 夹具的 daysLeft 是 hub 下发的值。时钟钉在离夹具几年之外的日期，按浏览器本地日期重算的实现在任何时区都与夹具
    // 不同而红；不钉时夹具恰好等于某一天的日历差，那一天本地重算照样全绿。只 fake Date，react-query 与 waitFor
    // 用的计时器保持真实。
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime("2030-06-15T12:00:00Z");
    });
    afterEach(() => { vi.useRealTimers(); });

    it("计费列合成价格、周期、到期与自动续期，已过期的那段标红，全空是破折号", async () => {
      renderNodes({ listNodes: async () => ({ nodes: [
        { ...two[0], billing: { price: "12.50", currency: "USD", billingCycle: BillingCycle.MONTHLY, expiresOn: "2026-10-01", daysLeft: 4, autoRenew: true } },
        { ...two[1], billing: { price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "2026-09-24", daysLeft: -3, autoRenew: false } },
        { ...two[0], id: 3n, name: "c" },
      ] }) });
      const row = async (name: string) => within((await screen.findByRole("link", { name })).closest("tr")!);
      expect((await row("a（#1）")).getByRole("cell", { name: "US$12.50 / 月 自动续期" })).toBeInTheDocument();
      // 日期进等宽 .num，中文的剩余天数不进；两段共用一个 data-level 着色。
      const soon = (await row("a（#1）")).getByText("2026-10-01");
      expect(soon).toHaveClass("num");
      expect(soon.closest(".expiry")).toHaveAttribute("data-level", "attention");
      expect(soon.closest(".expiry")).toHaveTextContent("2026-10-01剩 4 天");
      expect((await row("a（#1）")).getByText("剩 4 天")).not.toHaveClass("num");
      expect((await row("b（#2）")).getByText("已过期 3 天").closest(".expiry")).toHaveAttribute("data-level", "critical");
      expect((await row("c（#3）")).getAllByRole("cell")[column("费用")]).toHaveTextContent("—");
    });

    it.each(["CNY", "USD", "HKD", "CAD", "EUR", "GBP"])("币种平铺固定六种选项，选择 %s 后随计费整体提交", async (currency) => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      openRowAction("a（#1）", "编辑");
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "12.50" } });
      const currencies = screen.getByRole("group", { name: "币种" });
      expect(within(currencies).getAllByRole("radio").map((radio) => (radio as HTMLInputElement).labels?.[0]?.textContent)).toEqual(["未设置", "CNY", "USD", "HKD", "CAD", "EUR", "GBP"]);
      expect(within(currencies).getByRole("radio", { name: "未设置" })).toBeChecked();
      fireEvent.click(within(currencies).getByRole("radio", { name: currency }));
      const cycles = screen.getByRole("group", { name: "付款周期" });
      expect(within(cycles).getAllByRole("radio").map((radio) => (radio as HTMLInputElement).labels?.[0]?.textContent)).toEqual(["无周期", "每月", "每季", "每半年", "每年", "每两年", "每三年", "每五年"]);
      fireEvent.click(within(cycles).getByRole("radio", { name: "每年" }));
      fillSegments("到期日 a（#1）", "2027-01-31");
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      expect(within(currencies).getByRole("radio", { name: currency })).toBeChecked();
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
        id: 1n, name: "a", billing: objectContaining({ price: "12.50", currency, billingCycle: BillingCycle.YEARLY, expiresOn: "2027-01-31", autoRenew: true }),
      }), expect.anything()));
    });

    it("可选范围外的当前币种只读回显，不自动改写，仍可改选支持币种", async () => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: [{ ...two[0], billing: { price: "9", currency: "TWD" } }] }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
      const current = screen.getByRole("radio", { name: "TWD（当前值）" });
      expect(current).toBeChecked();
      expect(current).toBeDisabled();
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "10" } });
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ billing: objectContaining({ price: "10", currency: "TWD" }) }), expect.anything()));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      openRowAction("a（#1）", "编辑");
      fireEvent.click(screen.getByRole("radio", { name: "CNY" }));
      expect(screen.queryByRole("radio", { name: "TWD（当前值）" })).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenLastCalledWith(expect.objectContaining({ billing: objectContaining({ currency: "CNY" }) }), expect.anything()));
    });

    it("编辑从节点当前的计费开始，清空之后提交的是空值", async () => {
      const updateNode = vi.fn(async () => ({}));
      const current = { ...two[0], billing: { price: "9", currency: "EUR", billingCycle: BillingCycle.QUARTERLY, expiresOn: "2026-12-01", daysLeft: 60, autoRenew: true } };
      renderNodes({ listNodes: async () => ({ nodes: [current] }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      openRowAction("a（#1）", "编辑");
      expect({
        price: screen.getByLabelText<HTMLInputElement>("价格 a（#1）").value,
        currency: screen.getByRole<HTMLInputElement>("radio", { name: "EUR" }).checked,
        cycle: screen.getByRole<HTMLInputElement>("radio", { name: "每季" }).checked,
        expiresOn: segmentsValue("到期日 a（#1）"),
        autoRenew: screen.getByLabelText<HTMLInputElement>("自动续期 a（#1）").checked,
      }).toEqual({ price: "9", currency: true, cycle: true, expiresOn: "2026-12-01", autoRenew: true });
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "" } });
      fireEvent.click(screen.getByRole("radio", { name: "未设置" }));
      fireEvent.click(screen.getByRole("radio", { name: "无周期" }));
      fillSegments("到期日 a（#1）", "");
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({
        id: 1n, billing: objectContaining({ price: "", currency: "", billingCycle: BillingCycle.UNSPECIFIED, expiresOn: "", autoRenew: false }),
      }), expect.anything()));
    });

    it("hub 拒绝计费取值时显示错误原文，编辑行与五项草稿保留", async () => {
      const message = "billing.price: invalid decimal price";
      const updateNode = vi.fn(async () => { throw new ConnectError(message, Code.InvalidArgument); });
      renderNodes({ listNodes: async () => ({ nodes: two }), updateNode });
      await screen.findByRole("link", { name: "a（#1）" });
      openRowAction("a（#1）", "编辑");
      fireEvent.change(screen.getByLabelText("价格 a（#1）"), { target: { value: "abc" } });
      fireEvent.click(screen.getByRole("radio", { name: "USD" }));
      fireEvent.click(screen.getByRole("radio", { name: "每月" }));
      fillSegments("到期日 a（#1）", "2030-07-01");
      fireEvent.click(screen.getByLabelText("自动续期 a（#1）"));
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(message);
      expect(updateNode).toHaveBeenCalledTimes(1);
      expect({
        price: screen.getByLabelText<HTMLInputElement>("价格 a（#1）").value,
        currency: screen.getByRole<HTMLInputElement>("radio", { name: "USD" }).checked,
        cycle: screen.getByRole<HTMLInputElement>("radio", { name: "每月" }).checked,
        expiresOn: segmentsValue("到期日 a（#1）"),
        autoRenew: screen.getByLabelText<HTMLInputElement>("自动续期 a（#1）").checked,
      }).toEqual({ price: "abc", currency: true, cycle: true, expiresOn: "2030-07-01", autoRenew: true });
    });
  });

  it("宽限期编辑显示当前秒数与默认值", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    expect(screen.getByLabelText("离线宽限期（秒） a（#1）")).toHaveValue(90);
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    openRowAction("b（#2）", "编辑");
    expect(screen.getByLabelText("离线宽限期（秒） b（#2）")).toHaveValue(0);
  });

  it("编辑宽限期与非宽限期节点的保存载荷", async () => {
    const updateNode = vi.fn(async () => ({}));
    let releaseList!: () => void;
    const listGate = new Promise<void>((r) => { releaseList = r; });
    const listNodes = vi.fn(async () => {
      if (listNodes.mock.calls.length > 1) await listGate;
      return { nodes: two };
    });
    renderNodes({ listNodes, updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    const grace = screen.getByLabelText("离线宽限期（秒） a（#1）");
    expect(grace).toHaveValue(90);
    expect(grace).toHaveAccessibleDescription("0 表示取 hub 的 HERON_OFFLINE_AFTER；非 0 不能小于它。");
    fireEvent.change(grace, { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    try {
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, offlineGraceS: 120 }), expect.anything()));
      await waitFor(() => expect(listNodes).toHaveBeenCalledTimes(2));
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(rowAction("b（#2）", "编辑")).toHaveAttribute("aria-disabled", "true");
    } finally { await act(async () => { releaseList(); }); }
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    openRowAction("b（#2）", "编辑");
    fireEvent.change(screen.getByLabelText("名称 b（#2）"), { target: { value: "b2" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 2n, name: "b2", offlineGraceS: 0 }), expect.anything()));
  });

  it("已有非零宽限期可显式清除", async () => {
    const updateNode = vi.fn(async () => ({}));
    const listNodes = vi.fn()
      .mockResolvedValueOnce({ nodes: two })
      .mockResolvedValue({ nodes: [{ ...two[0], offlineGraceS: undefined }, two[1]] });
    renderNodes({ listNodes, updateNode });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("离线宽限期（秒） a（#1）"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, offlineGraceS: 0 }), expect.anything()));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    openRowAction("a（#1）", "编辑");
    expect(screen.getByLabelText("离线宽限期（秒） a（#1）")).toHaveValue(0);
  });

  it("非法宽限期禁用保存", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }) });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("离线宽限期（秒） a（#1）"), { target: { value: "-1" } });
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  });

  it("服务端宽限期下限错误显示原文", async () => {
    renderNodes({ listNodes: async () => ({ nodes: two }),
      updateNode: async () => { throw new ConnectError("offline_grace_s: must be 0 or at least 30 seconds (HERON_OFFLINE_AFTER); got 20", Code.InvalidArgument); } });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "编辑");
    fireEvent.change(screen.getByLabelText("离线宽限期（秒） a（#1）"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("offline_grace_s: must be 0 or at least 30 seconds (HERON_OFFLINE_AFTER); got 20");
  });

  it("轮换后显示并复制新 token", async () => {
    const rotateNodeToken = vi.fn(async () => ({ token: "new-token" }));
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    try {
      renderNodes({ listNodes: async () => ({ nodes: two }), rotateNodeToken });
      await screen.findByRole("link", { name: "a（#1）" });
      openRowAction("a（#1）", "换 token");
      await waitFor(() => expect(rotateNodeToken).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }), expect.anything()));
      expect(await screen.findByLabelText("节点 a（#1） 的新 token")).toHaveTextContent("new-token");
      fireEvent.click(screen.getByRole("button", { name: "复制" }));
      expect(await screen.findByRole("button", { name: "已复制" })).toBeInTheDocument();
      expect(writeText).toHaveBeenCalledWith("new-token");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("换 token 的弹窗里给出用新 token 的安装命令", async () => {
    const rotateNodeToken = vi.fn(async () => ({ token: "new-token" }));
    renderNodes({ listNodes: async () => ({ nodes: two }), rotateNodeToken });
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "换 token");
    expect(await screen.findByLabelText("节点 a（#1） 的新 token")).toHaveTextContent("new-token");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("curl 安装命令")).toHaveTextContent("--key new-token");
    expect(within(dialog).getByLabelText("wget 安装命令")).toHaveTextContent("--key new-token");
    for (const tool of ["curl", "wget"]) expect(within(dialog).getByLabelText(`${tool} 安装命令`)).toHaveTextContent("--re-register");
    expect(within(dialog).getByText(/替换本机原有的节点身份/)).toBeInTheDocument();
    expect(within(dialog).getByText(/普通升级保留现有配置/)).toBeInTheDocument();
    expect(within(dialog).getByText(/本地探测策略不会删除/)).toBeInTheDocument();
  });

  it("首次版本读取失败时凭据弹窗显示错误，恢复后才给出命令", async () => {
    let fail = true;
    const { queryClient } = renderNodes({
      listNodes: async () => ({ nodes: two }),
      rotateNodeToken: async () => ({ token: "new-token" }),
      getSnapshot: async () => {
        if (fail) throw new ConnectError("snapshot unavailable", Code.Unavailable);
        return { now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion: "v1.2.3" };
      },
    });
    await screen.findByRole("alert");
    await screen.findByRole("link", { name: "a（#1）" });
    openRowAction("a（#1）", "换 token");
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("alert")).toHaveTextContent("snapshot unavailable");
    expect(dialog.queryByText(/正在读取 hub 版本/)).toBeNull();
    expect(dialog.queryByLabelText("curl 安装命令")).toBeNull();
    expect(dialog.queryByLabelText("wget 安装命令")).toBeNull();
    fail = false;
    const snapshotKey = createConnectQueryKey({ schema: AdminService.method.getSnapshot, cardinality: "finite" });
    await act(async () => { await queryClient.refetchQueries({ queryKey: snapshotKey }); });
    for (const tool of ["curl", "wget"]) {
      expect(await dialog.findByLabelText(`${tool} 安装命令`)).toHaveTextContent("/releases/download/v1.2.3/install.sh");
      expect(dialog.getByLabelText(`${tool} 安装命令`)).toHaveTextContent("--re-register");
    }
    expect(dialog.queryByRole("alert")).toBeNull();
  });

  describe("标签", () => {
    const tagged = [
      { id: 1n, name: "alpha", public: false, note: "", sortOrder: 0, createdAt: 0n, trafficResetDay: 1, tags: ["db"] },
      { id: 2n, name: "beta", public: false, note: "", sortOrder: 1, createdAt: 0n, trafficResetDay: 1, tags: ["db", "web"] },
      { id: 3n, name: "gamma", public: false, note: "", sortOrder: 2, createdAt: 0n, trafficResetDay: 1, tags: ["web"] },
      { id: 4n, name: "delta", public: false, note: "", sortOrder: 3, createdAt: 0n, trafficResetDay: 1, tags: [] },
    ];
    // 按 hub 的语义应答：untagged 为真只返回无标签节点且与非空 tags 互斥（同时给出按参数错误拒绝），否则返回
    // 同时带有全部所选标签的节点，名字大小写不敏感，空选择返回全部。数据集可替换，供需要额外标签的用例复用同一替身。
    const listByTags = (data: typeof tagged = tagged) => vi.fn(async (req: ListNodesRequest) => {
      if (req.untagged) {
        if (req.tags.length > 0) throw new ConnectError("untagged 与非空 tags 互斥", Code.InvalidArgument);
        return { nodes: data.filter((n) => n.tags.length === 0) };
      }
      return { nodes: data.filter((n) => req.tags.every((t) => n.tags.some((x) => sameTag(x, t)))) };
    });
    const tagList = async () => ({ tags: [{ name: "db", nodeCount: 2 }, { name: "web", nodeCount: 2 }] });
    const shown = () => screen.queryAllByRole("link").map((link) => link.textContent);

    // "无标签"选项的可访问名称与标签项分开，才能与名为"无标签"的标签同时定位。
    const untaggedBox = () => screen.getByRole("checkbox", { name: "只看没有标签的节点" });

    it("名称格显示节点标签，没有标签时不渲染列表", async () => {
      renderNodes({ listNodes: async () => ({ nodes: [tagged[1], two[0]] }), listTags: tagList });
      const row = async (name: string) => within((await screen.findByRole("link", { name })).closest("tr")!);
      expect((await row("beta（#2）")).getByLabelText("标签 beta（#2）")).toHaveTextContent(/^dbweb$/);
      expect((await row("a（#1）")).queryByRole("list", { name: "标签 a（#1）" })).toBeNull();
    });

    it("多选过滤按所选全部标签请求 hub 并列出交集，过滤中禁用排序，清除后恢复全部", async () => {
      const listNodes = listByTags();
      const reorderNodes = vi.fn(async () => ({}));
      renderNodes({ listNodes, listTags: tagList, reorderNodes });
      await screen.findByRole("link", { name: "gamma（#3）" });
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [] }), expect.anything());
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta"]));
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: ["db"] }), expect.anything());
      fireEvent.click(filterBox("web"));
      await waitFor(() => expect(shown()).toEqual(["beta"]));
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: ["db", "web"] }), expect.anything());
      expect(screen.getByText("筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。")).toBeInTheDocument();
      for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) {
        expect(button).toBeDisabled();
        fireEvent.click(button);
      }
      await act(async () => {});
      expect(reorderNodes).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta", "gamma", "delta"]));
      for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) expect(button).toBeEnabled();
      openRowAction("alpha（#1）", "下移一位");
      await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 1n, 3n, 4n] }), expect.anything()));
    });

    it("标签过滤与搜索框取交集：只列同时满足两者的节点", async () => {
      renderNodes({ listNodes: listByTags(), listTags: tagList });
      await screen.findByRole("link", { name: "gamma（#3）" });
      const search = screen.getByRole("searchbox", { name: "搜索节点" });
      fireEvent.change(search, { target: { value: "alpha" } });
      expect(shown()).toEqual(["alpha"]);
      fireEvent.click(filterBox("web"));
      // alpha 命中搜索但没有 web 标签；web 的节点（beta、gamma）不命中搜索。
      await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("没有匹配的节点。"));
      expect(shown()).toEqual([]);
      fireEvent.change(search, { target: { value: "GAMMA" } });
      expect(shown()).toEqual(["gamma"]);
      fireEvent.change(search, { target: { value: "" } });
      expect(shown()).toEqual(["beta", "gamma"]);
    });

    it("编辑：输入新建标签、按折叠去重、移除，输入框里未添加的文字随保存提交", async () => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: [tagged[0]] }), listTags: tagList, updateNode });
      await screen.findByRole("link", { name: "alpha（#1）" });
      openRowAction("alpha（#1）", "编辑");
      const input = screen.getByRole("combobox", { name: "新标签 alpha（#1）" });
      fireEvent.change(input, { target: { value: " web " } });
      fireEvent.keyDown(input, { key: "Enter" });
      fireEvent.change(input, { target: { value: "WEB" } });
      fireEvent.click(screen.getByRole("button", { name: "添加标签 alpha（#1）" }));
      fireEvent.click(screen.getByRole("button", { name: "移除标签 db alpha（#1）" }));
      fireEvent.change(input, { target: { value: "客户A" } });
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, tags: ["web", "客户A"] }), expect.anything()));
    });

    it("UpdateNode 整体替换，只改名称时提交体里的标签必须是节点当前的标签", async () => {
      const updateNode = vi.fn(async () => ({}));
      renderNodes({ listNodes: async () => ({ nodes: [tagged[1]] }), listTags: tagList, updateNode });
      await screen.findByRole("link", { name: "beta（#2）" });
      openRowAction("beta（#2）", "编辑");
      fireEvent.change(screen.getByLabelText("名称 beta（#2）"), { target: { value: "renamed" } });
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 2n, name: "renamed", tags: ["db", "web"] }), expect.anything()));
    });

    it("标签管理：确认后删除标签，刷新列表并从过滤条件里去掉它", async () => {
      let tags = [{ name: "db", nodeCount: 2 }, { name: "web", nodeCount: 2 }];
      const deleteTag = vi.fn(async (req: { name: string }) => { tags = tags.filter((t) => t.name !== req.name); return {}; });
      const listNodes = listByTags();
      renderNodes({ listNodes, listTags: async () => ({ tags }), deleteTag });
      const manager = within(await screen.findByRole("region", { name: "标签" }));
      expect(manager.getByText("db").parentElement).toHaveTextContent("db 2 个节点");
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta"]));
      fireEvent.click(manager.getByRole("button", { name: "删除标签 db" }));
      expect(deleteTag).not.toHaveBeenCalled();
      fireEvent.click(manager.getByRole("button", { name: "确认删除标签 db" }));
      await waitFor(() => expect(deleteTag).toHaveBeenCalledWith(expect.objectContaining({ name: "db" }), expect.anything()));
      await waitFor(() => expect(screen.queryByRole("checkbox", { name: "db" })).toBeNull());
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta", "gamma", "delta"]));
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [] }), expect.anything());
      expect(manager.queryByText("db")).toBeNull();
    });

    it("已选的标签在别处被删掉后仍列在过滤器里并可取消", async () => {
      let tags = [{ name: "db", nodeCount: 2 }, { name: "web", nodeCount: 2 }];
      const listNodes = listByTags();
      renderNodes({ listNodes, listTags: async () => ({ tags }), updateNode: async () => { tags = [tags[1]]; return {}; } });
      await screen.findByRole("link", { name: "gamma（#3）" });
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta"]));
      openRowAction("alpha（#1）", "编辑");
      fireEvent.click(screen.getByRole("button", { name: "保存" }));
      await waitFor(() => expect(screen.queryByRole("region", { name: "标签" })).not.toHaveTextContent("db"));
      expect(filterBox("db")).toBeChecked();
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta", "gamma", "delta"]));
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [] }), expect.anything());
    });

    // 过滤条件是查询的输入：新条件的请求失败时它们必须还在，用户才能改回去。hub 的错误原文照常显示，
    // 页面不另抄"至多 16 个"的规则，第 17 个照样发给 hub。
    it("勾满 17 个标签，hub 回 InvalidArgument：错误原文可见，过滤器仍在，取消一个即恢复", async () => {
      const seventeen = Array.from({ length: 17 }, (_, i) => ({ name: `t${i}`, nodeCount: i === 0 ? 1 : 0 }));
      const alpha = { ...tagged[0], tags: ["t0"] };
      const listNodes = vi.fn(async (req: ListNodesRequest) => {
        if (req.tags.length > 16) throw new ConnectError(`tags: at most 16 distinct tags (case-insensitive); got ${req.tags.length}`, Code.InvalidArgument);
        return { nodes: req.tags.every((t) => t === "t0") ? [alpha] : [] };
      });
      renderNodes({ listNodes, listTags: async () => ({ tags: seventeen }) });
      await screen.findByRole("link", { name: "alpha（#1）" });
      for (const t of seventeen) {
        fireEvent.click(filterBox(t.name));
        await waitFor(() => expect(filterBox(t.name)).toBeChecked());
      }
      expect(await screen.findByRole("alert")).toHaveTextContent("tags: at most 16 distinct tags (case-insensitive); got 17");
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: seventeen.map((t) => t.name) }), expect.anything());
      fireEvent.click(filterBox("t16"));
      await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
      expect(filterBox("t16")).not.toBeChecked();
      expect(screen.getByRole("status")).toHaveTextContent("没有匹配的节点。");
    });

    it("单个标签过滤时 hub 暂时不可用：错误原文可见，过滤器、上一份结果与未保存的草稿都在", async () => {
      const listNodes = vi.fn(async (req: ListNodesRequest) => {
        if (req.tags.length > 0) throw new ConnectError("hub unavailable", Code.Unavailable);
        return { nodes: tagged };
      });
      renderNodes({ listNodes, listTags: tagList });
      await screen.findByRole("link", { name: "gamma（#3）" });
      openRowAction("alpha（#1）", "编辑");
      const input = screen.getByLabelText("名称 alpha（#1）");
      fireEvent.change(input, { target: { value: "尚未保存" } });
      fireEvent.click(filterBox("web"));
      expect(await screen.findByRole("alert")).toHaveTextContent("hub unavailable");
      expect(filterBox("web")).toBeChecked();
      expect(screen.getByLabelText("名称 alpha（#1）")).toBe(input);
      expect(input).toHaveValue("尚未保存");
      expect(shown()).toEqual(["alpha", "beta", "gamma", "delta"]);
      fireEvent.click(filterBox("web"));
      await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
      expect(screen.getByLabelText("名称 alpha（#1）")).toBe(input);
      expect(input).toHaveValue("尚未保存");
    });

    // 排序要求全部 id 的完整排列。沿用的结果属于上一个条件，即使当前条件为空，它也可能只是子集。
    it("当前条件还没有自己的结果时，沿用的上一份结果不开放排序", async () => {
      let fail = false;
      const byTags = listByTags();
      const listNodes = vi.fn(async (req: ListNodesRequest) => {
        if (fail) throw new ConnectError("hub unavailable", Code.Unavailable);
        return byTags(req);
      });
      const reorderNodes = vi.fn(async () => ({}));
      const { queryClient } = renderNodes({ listNodes, listTags: tagList, reorderNodes });
      await screen.findByRole("link", { name: "gamma（#3）" });
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta"]));
      // 空条件的缓存已回收（离开它超过 gcTime），清除过滤后的请求又失败：显示的仍是 db 的结果。
      queryClient.removeQueries({ predicate: (q) => {
        const key = q.queryKey[1] as { methodName?: string; input?: object };
        return key.methodName === "ListNodes" && Object.keys(key.input ?? {}).length === 0;
      } });
      fail = true;
      fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("hub unavailable");
      expect(shown()).toEqual(["alpha", "beta"]);
      expect(screen.getByText("列表还不是当前条件下的结果，暂时无法排序。")).toBeInTheDocument();
      for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) {
        expect(button).toBeDisabled();
        fireEvent.click(button);
      }
      await act(async () => {});
      expect(reorderNodes).not.toHaveBeenCalled();
    });

    it("标签清单取不到时节点列表照常显示，过滤器处说明原因", async () => {
      renderNodes({ listNodes: async () => ({ nodes: tagged }), listTags: async () => { throw new ConnectError("tags unavailable", Code.Unavailable); } });
      await screen.findByRole("link", { name: "gamma（#3）" });
      expect(await screen.findByRole("alert")).toHaveTextContent("无法取得标签清单：");
      expect(screen.getByRole("alert")).toHaveTextContent("tags unavailable");
      expect(screen.queryByRole("region", { name: "标签" })).toBeNull();
    });

    it("勾'无标签'只列无标签节点，请求带 untagged 不带标签，排序禁用", async () => {
      const listNodes = listByTags();
      const reorderNodes = vi.fn(async () => ({}));
      renderNodes({ listNodes, listTags: tagList, reorderNodes });
      await screen.findByRole("link", { name: "delta（#4）" });
      fireEvent.click(untaggedBox());
      await waitFor(() => expect(shown()).toEqual(["delta"]));
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [], untagged: true }), expect.anything());
      expect(screen.getByText("筛选时不能用拖动或上下移（它们保存完整排列）；可用行菜单的「移动到…」按全序名次移动，或清除筛选后再调整。")).toBeInTheDocument();
      for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) {
        expect(button).toBeDisabled();
        fireEvent.click(button);
      }
      await act(async () => {});
      expect(reorderNodes).not.toHaveBeenCalled();
    });

    it("已选标签后勾'无标签'清掉标签选择，只按无标签请求", async () => {
      const listNodes = listByTags();
      renderNodes({ listNodes, listTags: tagList });
      await screen.findByRole("link", { name: "delta（#4）" });
      fireEvent.click(filterBox("db"));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta"]));
      fireEvent.click(untaggedBox());
      await waitFor(() => expect(shown()).toEqual(["delta"]));
      expect(filterBox("db")).not.toBeChecked();
      expect(filterBox("web")).not.toBeChecked();
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [], untagged: true }), expect.anything());
    });

    it("无标签状态下勾一个标签：'无标签'取消，只按该标签请求", async () => {
      const listNodes = listByTags();
      renderNodes({ listNodes, listTags: tagList });
      await screen.findByRole("link", { name: "delta（#4）" });
      fireEvent.click(untaggedBox());
      await waitFor(() => expect(shown()).toEqual(["delta"]));
      fireEvent.click(filterBox("web"));
      await waitFor(() => expect(shown()).toEqual(["beta", "gamma"]));
      expect(untaggedBox()).not.toBeChecked();
      expect(filterBox("web")).toBeChecked();
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: ["web"], untagged: false }), expect.anything());
    });

    it("无标签状态下'清除筛选'可见，清除后恢复全部并恢复排序", async () => {
      const listNodes = listByTags();
      renderNodes({ listNodes, listTags: tagList });
      await screen.findByRole("link", { name: "delta（#4）" });
      fireEvent.click(untaggedBox());
      await waitFor(() => expect(shown()).toEqual(["delta"]));
      fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
      await waitFor(() => expect(shown()).toEqual(["alpha", "beta", "gamma", "delta"]));
      expect(untaggedBox()).not.toBeChecked();
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: [], untagged: false }), expect.anything());
      for (const button of screen.getAllByRole("button", { name: /^调整顺序/ })) expect(button).toBeEnabled();
    });

    // 用户可能真的建一个叫"无标签"的标签；它的可访问名与"只看没有标签的节点"必须分开，勾它走标签过滤而不是 untagged。
    it("名为'无标签'的标签与'无标签'选项可分别勾选，勾标签不触发 untagged", async () => {
      const named = { id: 9n, name: "named", public: false, note: "", sortOrder: 0, createdAt: 0n, trafficResetDay: 1, tags: ["无标签"] };
      const listNodes = listByTags([...tagged, named]);
      renderNodes({ listNodes, listTags: async () => ({ tags: [{ name: "无标签", nodeCount: 1 }] }) });
      await screen.findByRole("link", { name: "named（#9）" });
      expect(untaggedBox()).not.toBeChecked();
      fireEvent.click(filterBox("无标签"));
      await waitFor(() => expect(shown()).toEqual(["named"]));
      expect(untaggedBox()).not.toBeChecked();
      expect(listNodes).toHaveBeenLastCalledWith(expect.objectContaining({ tags: ["无标签"], untagged: false }), expect.anything());
    });

    it("已选中节点时切换'无标签'会清空选择", async () => {
      renderNodes({ listNodes: listByTags(), listTags: tagList });
      await screen.findByRole("link", { name: "delta（#4）" });
      fireEvent.click(screen.getByRole("checkbox", { name: "选择 delta（#4）" }));
      expect(screen.getByText("已选择 1 个节点")).toBeInTheDocument();
      fireEvent.click(untaggedBox());
      await waitFor(() => expect(screen.queryByRole("toolbar", { name: "批量操作" })).toBeNull());
    });
  });
});

it("编辑公开节点时提示标签对访客可见，取消公开即不再提示", async () => {
  renderNodes({ listNodes: async () => ({ nodes: two }) });
  await screen.findByRole("link", { name: "a（#1）" });
  openRowAction("a（#1）", "编辑");
  const box = screen.getByLabelText<HTMLInputElement>("公开 a（#1）");
  const hint = "公开节点的标签在公开页对访客可见。";
  expect(box.checked).toBe(false);
  expect(screen.queryByText(hint)).toBeNull();
  fireEvent.click(box);
  expect(screen.getByText(hint)).toBeInTheDocument();
  fireEvent.click(box);
  expect(screen.queryByText(hint)).toBeNull();
});

it("维护中的节点在状态列标注，编辑里的维护开关随整体替换提交", async () => {
  const updateNode = vi.fn(async () => ({}));
  renderNodes({ listNodes: async () => ({ nodes: [{ ...two[0], maintenance: true }, two[1]] }), updateNode });
  await screen.findByRole("link", { name: "b（#2）" });
  expect(within(screen.getByRole("row", { name: "a" })).getByText("维护中")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "a（#1）" })).not.toHaveTextContent("维护中");
  expect(screen.getByRole("link", { name: "b（#2）" }).closest("tr")).not.toHaveTextContent("维护中");
  openRowAction("b（#2）", "编辑");
  const box = screen.getByLabelText<HTMLInputElement>("维护 b（#2）");
  expect(box.checked).toBe(false);
  fireEvent.click(box);
  fireEvent.click(screen.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 2n, maintenance: true }), expect.anything()));
});

describe("筛选由 URL 持有", () => {
  const tagged = [
    { ...two[0], tags: ["db"] },
    { ...two[1], tags: [] },
  ];
  const routes = [{ path: "/nodes", Component: Nodes }, { path: "/nodes/:id", Component: () => <p>详情页</p> }];
  const open = (path: string, impl: AdminImpl = {}) => renderWithAdmin({
    getSnapshot: snapshotOf("v1.1.0"), listTags: async () => ({ tags: [{ name: "db", nodeCount: 1 }] }), listNodes: async () => ({ nodes: tagged }), ...impl,
  }, routes, path);
  const search = (router: { state: { location: { search: string } } }) => new URLSearchParams(router.state.location.search);

  it("带筛选的 URL 打开即生效：搜索词回填，URL 里大小写不同的标签按清单写法请求并显示胶囊", async () => {
    const requests: ListNodesRequest[] = [];
    open("/nodes?q=a&tag=DB", { listNodes: async (request: ListNodesRequest) => { requests.push(request); return { nodes: tagged }; } });
    expect(await screen.findByRole("searchbox", { name: "搜索节点" })).toHaveValue("a");
    expect(await screen.findByRole("button", { name: "移除 db" })).toBeInTheDocument();
    await waitFor(() => expect(requests.at(-1)?.tags).toEqual(["db"]));
    expect(screen.getByRole("link", { name: withIdLabel("a", 1n) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: withIdLabel("b", 2n) })).toBeNull();
  });

  it("搜索、标签、无标签与状态的改动写进 URL；无标签与标签同时出现时按无标签", async () => {
    const { router } = open("/nodes?untagged=1&tag=db");
    expect(await screen.findByRole("checkbox", { name: "只看没有标签的节点" })).toBeChecked();
    fireEvent.click(screen.getByRole("checkbox", { name: "只看没有标签的节点" }));
    await waitFor(() => expect(search(router).get("untagged")).toBeNull());
    expect(search(router).getAll("tag")).toEqual([]);
    fireEvent.click(await findFilterBox("db"));
    await waitFor(() => expect(search(router).getAll("tag")).toEqual(["db"]));
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索节点" }), { target: { value: "a" } });
    await waitFor(() => expect(search(router).get("q")).toBe("a"));
    chooseOption("状态", "离线");
    await waitFor(() => expect(search(router).get("status")).toBe("offline"));
    expect(search(router).get("q")).toBe("a");
    expect(search(router).getAll("tag")).toEqual(["db"]);
    fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
    await waitFor(() => expect(router.state.location.search).toBe(""));
    expect(screen.getByRole("searchbox", { name: "搜索节点" })).toHaveValue("");
  });

  it("URL 里清单没有的标签显示为可移除的胶囊，移除后 URL 不再带它", async () => {
    const { router } = open("/nodes?tag=gone");
    fireEvent.click(await screen.findByRole("button", { name: "移除 gone" }));
    await waitFor(() => expect(search(router).getAll("tag")).toEqual([]));
  });

  it("名称链接与「查看详情」都把列表的查询串带进详情的导航 state", async () => {
    const { router } = open("/nodes?q=a&tag=db");
    fireEvent.click(await screen.findByRole("link", { name: withIdLabel("a", 1n) }));
    await screen.findByText("详情页");
    expect(router.state.location.state).toEqual({ nodeListSearch: "?q=a&tag=db" });
    await act(async () => { await router.navigate("/nodes?q=b"); });
    openRowAction(withIdLabel("b", 2n), "查看详情");
    await screen.findByText("详情页");
    expect(router.state.location.state).toEqual({ nodeListSearch: "?q=b" });
  });
});

describe("移动到指定位置", () => {
  // a、c 挂 db，b、d 挂 web；position 是全序名次（服务端随读算出，过滤不改变）。
  const positioned = [
    { id: 1n, name: "a", public: false, note: "", sortOrder: 0, createdAt: 0n, trafficResetDay: 1, tags: ["db"], position: 1 },
    { id: 2n, name: "b", public: false, note: "", sortOrder: 1, createdAt: 0n, trafficResetDay: 1, tags: ["web"], position: 2 },
    { id: 3n, name: "c", public: false, note: "", sortOrder: 2, createdAt: 0n, trafficResetDay: 1, tags: ["db"], position: 3 },
    { id: 4n, name: "d", public: false, note: "", sortOrder: 3, createdAt: 0n, trafficResetDay: 1, tags: ["web"], position: 4 },
  ];
  const tagList = async () => ({ tags: [{ name: "db", nodeCount: 2 }, { name: "web", nodeCount: 2 }] });
  const listHub = vi.fn(async (req: ListNodesRequest) => {
    if (req.tags.length === 0) return { nodes: positioned };
    return { nodes: positioned.filter((n) => req.tags.every((t) => n.tags.some((x) => sameTag(x, t)))) };
  });
  // 行首序号：手柄按钮里唯一的文字就是序号。
  const positionOf = (label: string) => screen.getByRole("button", { name: `调整顺序 ${label}` }).textContent;

  it("排序说明写出全部入口，含行菜单与批量的「移动到…」", async () => {
    renderNodes({ listNodes: listHub, listTags: tagList });
    await screen.findByRole("link", { name: "a（#1）" });
    const help = document.getElementById("node-order-help")!;
    expect(help).toHaveTextContent("上移、下移、置顶、置底或「移动到…」指定位置");
    expect(help).toHaveTextContent("勾选多个节点后可批量「移动到…」");
  });

  it("未过滤时序号是当前位次，过滤时序号是服务端全序名次", async () => {
    renderNodes({ listNodes: listHub, listTags: tagList });
    await screen.findByRole("link", { name: "a（#1）" });
    expect(positionOf("a（#1）")).toBe("1");
    expect(positionOf("d（#4）")).toBe("4");
    fireEvent.click(filterBox("db"));
    await waitFor(() => expect(screen.queryByRole("link", { name: "b（#2）" })).toBeNull());
    expect(positionOf("a（#1）")).toBe("1");
    expect(positionOf("c（#3）")).toBe("3");
  });

  it("显示的不是当前完整列表时序号用服务端名次，不按行下标", async () => {
    // 完整列表 {tags: []} 一直读不到自己的数据：先选 db 看到 a、c，取消后沿用旧结果，
    // 行还是 a、c，序号必须是全序名次 1、3 而不是下标 1、2。
    const listNodes = vi.fn((req: ListNodesRequest) => {
      if (req.tags.length === 0) return new Promise<never>(() => {});
      return Promise.resolve({ nodes: positioned.filter((n) => req.tags.every((t) => n.tags.some((x) => sameTag(x, t)))) });
    });
    renderNodes({ listNodes, listTags: tagList });
    await findFilterBox("db");
    fireEvent.click(filterBox("db"));
    await screen.findByRole("link", { name: "c（#3）" });
    fireEvent.click(filterBox("db"));
    await screen.findByText("列表还不是当前条件下的结果，暂时无法排序。");
    expect(screen.queryByRole("link", { name: "b（#2）" })).toBeNull();
    expect(positionOf("a（#1）")).toBe("1");
    expect(positionOf("c（#3）")).toBe("3");
  });

  it("未过滤且拖动保存未确认时，序号是期望排列的位次而不是服务端名次", async () => {
    const reorderNodes = vi.fn((): Promise<never> => new Promise(() => {}));
    renderNodes({ listNodes: listHub, listTags: tagList, reorderNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    openRowAction("a（#1）", "下移一位");
    await waitFor(() => expect(screen.getByText("正在保存并确认排序…")).toBeInTheDocument());
    // a 的服务端 position 仍是 1，序号按期望排列显示为 2。
    expect(positionOf("a（#1）")).toBe("2");
    expect(positionOf("b（#2）")).toBe("1");
  });

  it("MoveNodes 在途时拖动与菜单上下移都关闭", async () => {
    const moveNodes = vi.fn((): Promise<never> => new Promise(() => {}));
    const reorderNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes, reorderNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 c（#3）" }));
    fireEvent.click(screen.getByRole("button", { name: "移动到…" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "移动" }));
    // 提交后 MoveNodes 一直未返回：手柄禁用，菜单上下移不再触发 ReorderNodes。
    await waitFor(() => expect(screen.getByRole("button", { name: "调整顺序 a（#1）" })).toBeDisabled());
    openRowAction("a（#1）", "下移一位");
    expect(reorderNodes).not.toHaveBeenCalled();
    expect(moveNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [3n], position: 1 }), expect.anything());
  });

  it("过滤时弹窗的区间仍按节点总数计算，不用可见行数", async () => {
    const moveNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(filterBox("db"));
    await waitFor(() => expect(screen.queryByRole("link", { name: "b（#2）" })).toBeNull());
    // 可见只剩 a、c 两行；N = 4、k = 1，上限仍是 4。
    openRowAction("c（#3）", "移动到…");
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("目标位置（1–4）")).toHaveAttribute("max", "4");
  });

  it("多选两个节点经弹窗提交 ids 与 position，成功后清空选择并刷新列表", async () => {
    const listNodes = vi.fn(listHub.getMockImplementation());
    const moveNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 b（#2）" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 d（#4）" }));
    expect(screen.getByText("已选择 2 个节点")).toBeInTheDocument();
    const calls = listNodes.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "移动到…" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("共 4 个节点，将移动其中的 2 个；其余节点相对顺序不变。")).toBeInTheDocument();
    // N = 4、k = 2：max = N-k+1 = 3，预览随输入更新。
    const input = within(dialog).getByLabelText("目标位置（1–3）");
    expect(input).toHaveAttribute("max", "3");
    expect(within(dialog).getByText("将 2 个节点移到第 1–2 位")).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "3" } });
    expect(within(dialog).getByText("将 2 个节点移到第 3–4 位")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "移动" }));
    await waitFor(() => expect(moveNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 4n], position: 3 }), expect.anything()));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByRole("toolbar", { name: "批量操作" })).toBeNull();
    await waitFor(() => expect(listNodes.mock.calls.length).toBeGreaterThan(calls));
  });

  it("行菜单「移动到…」作用于单个节点，预览与区间按 k=1 计算", async () => {
    const moveNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    openRowAction("b（#2）", "移动到…");
    const dialog = screen.getByRole("dialog");
    const input = within(dialog).getByLabelText("目标位置（1–4）");
    expect(input).toHaveAttribute("max", "4");
    expect(within(dialog).getByText("移到第 1 位")).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "4" } });
    expect(within(dialog).getByText("移到第 4 位")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "移动" }));
    await waitFor(() => expect(moveNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n], position: 4 }), expect.anything()));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("区间外的输入拦截在弹窗内，不给 hub 发请求", async () => {
    const moveNodes = vi.fn(async () => ({}));
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 c（#3）" }));
    fireEvent.click(screen.getByRole("button", { name: "移动到…" }));
    const dialog = screen.getByRole("dialog");
    const input = within(dialog).getByLabelText("目标位置（1–4）");
    fireEvent.change(input, { target: { value: "5" } });
    expect(within(dialog).getByText("目标位置必须是 1–4 之间的整数。")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "移动" })).toBeDisabled();
    fireEvent.submit(within(dialog).getByLabelText("目标位置（1–4）").closest("form")!);
    await act(async () => {});
    expect(moveNodes).not.toHaveBeenCalled();
  });

  it("提交失败显示在弹窗内并保持打开，可取消", async () => {
    const moveNodes = vi.fn(async () => { throw new ConnectError("position: must be between 1 and 4", Code.InvalidArgument); });
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 c（#3）" }));
    fireEvent.click(screen.getByRole("button", { name: "移动到…" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "移动" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("alert")).toHaveTextContent("position: must be between 1 and 4");
    expect(screen.getByText("已选择 1 个节点")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText("已选择 1 个节点")).toBeInTheDocument();
  });

  it("拖动排序保存未确认时「移动到…」入口禁用", async () => {
    const moveNodes = vi.fn(async () => ({}));
    // 保存不结束：排序会话停在未确认（order.pending）。
    const reorderNodes = vi.fn((): Promise<never> => new Promise(() => {}));
    renderNodes({ listNodes: listHub, listTags: tagList, moveNodes, reorderNodes });
    await screen.findByRole("link", { name: "d（#4）" });
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 c（#3）" }));
    openRowAction("a（#1）", "下移一位");
    await waitFor(() => expect(screen.getByText("正在保存并确认排序…")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "移动到…" })).toBeDisabled();
    // 排序会话本身允许继续累积调整，但「移动到…」在未确认前关闭。
    expect(rowAction("a（#1）", "移动到…")).toHaveAttribute("aria-disabled", "true");
  });

  it("全部节点总数读取失败时入口禁用并说明原因", async () => {
    const moveNodes = vi.fn(async () => ({}));
    // 初始成功、之后完整列表读取失败：主列表可用而 N 拿不到，入口应禁用并说明原因。
    let broken = false;
    const listNodes = vi.fn(async (req: ListNodesRequest) => {
      if (req.tags.length === 0) {
        if (broken) throw new ConnectError("无法读取节点总数", Code.Unavailable);
        return { nodes: positioned };
      }
      return { nodes: positioned.filter((n) => req.tags.every((t) => n.tags.some((x) => sameTag(x, t)))) };
    });
    const { queryClient } = renderNodes({ listNodes, listTags: tagList, moveNodes });
    await screen.findByRole("link", { name: "c（#3）" });
    fireEvent.click(filterBox("db"));
    await waitFor(() => expect(screen.queryByRole("link", { name: "b（#2）" })).toBeNull());
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 c（#3）" }));
    expect(screen.getByRole("button", { name: "移动到…" })).toBeEnabled();
    broken = true;
    const fullKey = createConnectQueryKey({ schema: AdminService.method.listNodes, input: { tags: [] }, cardinality: "finite" });
    await act(async () => { await queryClient.refetchQueries({ queryKey: fullKey }); });
    await waitFor(() => expect(screen.getByRole("button", { name: "移动到…" })).toBeDisabled());
    expect(rowAction("a（#1）", "移动到…")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText(/无法取得节点总数，「移动到…」不可用/)).toBeInTheDocument();
    expect(moveNodes).not.toHaveBeenCalled();
  });
});

it("编辑抽屉四个分组按设计顺序，费用分组与原计费入口同一套字段", async () => {
  renderNodes({ listNodes: async () => ({ nodes: two }), updateNode: async () => ({}) });
  await screen.findByRole("link", { name: "a（#1）" });
  openRowAction("a（#1）", "编辑");
  const dialog = screen.getByRole("dialog");
  expect(dialog).toHaveClass("drawer");
  expect(within(dialog).getAllByRole("region").map((section) => section.getAttribute("aria-label") ?? within(section).getByRole("heading").textContent)).toEqual(["基本", "地区", "费用", "运行"]);
  expect(within(within(dialog).getByRole("region", { name: "费用" })).getByLabelText("价格 a（#1）")).toBeInTheDocument();
  expect(within(within(dialog).getByRole("region", { name: "运行" })).getByLabelText("维护 a（#1）")).toBeInTheDocument();
});

it("没有节点时只有空态卡，不画只有表头的表", async () => {
  renderNodes({ listNodes: async () => ({ nodes: [] }) });
  await expectEmptyState("还没有节点。", { region: "节点管理" });
  expect(screen.getByText("添加节点后安装 agent 即可开始监控。")).toBeInTheDocument();
});
