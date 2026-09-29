import { expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithAdmin } from "../test/harness";
import { type SaveProbeTaskRequest } from "../gen/probe/v1/admin_pb";
import { ProbeTasks } from "./ProbeTasks";

it("标签批量选择保存固定节点，动态选择器只保存非空标签", async () => {
  const saved: SaveProbeTaskRequest[] = [];
  renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 1n, name: "数据库", tags: ["db", "west"] }, { id: 2n, name: "应用", tags: ["west"] }] }),
    listTags: async () => ({ tags: [{ name: "db" }, { name: "west" }] }),
    listProbeTasks: async () => ({}),
    saveProbeTask: async (req) => { saved.push(req); return {}; },
  }, [{ path: "/probes", Component: ProbeTasks }], "/probes");
  let form = await screen.findByRole("form", { name: "新建探测任务" });
  fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "192.0.2.1" } });
  fireEvent.change(within(form).getByLabelText("按标签筛选（交集）"), { target: { value: "db" } });
  fireEvent.click(within(form).getByRole("button", { name: "选择筛选结果（1）" }));
  fireEvent.submit(form);
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0]).toMatchObject({ nodeIds: [1n], selectorTags: [], allNodes: false });
  await waitFor(() => expect(within(screen.getByRole("form", { name: "新建探测任务" })).getByLabelText("目标")).toHaveValue(""));
  form = screen.getByRole("form", { name: "新建探测任务" });
  fireEvent.change(within(form).getByLabelText("目标"), { target: { value: "192.0.2.2" } });
  fireEvent.click(within(form).getByLabelText("动态标签选择器"));
  fireEvent.submit(form);
  expect(saved).toHaveLength(1);
  fireEvent.change(within(form).getByLabelText("动态匹配标签（交集）"), { target: { value: "west" } });
  fireEvent.submit(form);
  await waitFor(() => expect(saved).toHaveLength(2));
  expect(saved[1]).toMatchObject({ nodeIds: [], selectorTags: ["west"], allNodes: false });
});
