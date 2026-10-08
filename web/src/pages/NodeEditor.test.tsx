import { create } from "@bufbuild/protobuf";
import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { NodeSchema } from "../gen/heron/v1/admin_pb";
import { TrafficQuotaMode } from "../gen/heron/v1/types_pb";
import { NodeEditor } from "./NodeEditor";

afterEach(cleanup);

function editor(bytes = 1073741825n) {
  const onSave = vi.fn();
  const node = create(NodeSchema, { id: 1n, name: "quota", trafficResetDay: 1, trafficQuotaBytes: bytes, trafficQuotaMode: TrafficQuotaMode.TX });
  render(<NodeEditor node={node} knownTags={[]} saving={false} error={null} listError={null} onClose={() => {}} onSave={onSave} opener={document.createElement("button")} />);
  return onSave;
}

it("preserves exact untouched bytes instead of round-tripping the displayed value", () => {
  const save = editor();
  fireEvent.click(screen.getByRole("button", { name: "保存" }));
  expect(save.mock.calls[0][0]).toMatchObject({ trafficQuotaBytes: 1073741825n, trafficQuotaMode: TrafficQuotaMode.TX });
});

it("converts edited decimal input and changed unit", () => {
  const save = editor();
  fireEvent.change(screen.getByLabelText(/流量配额 quota/), { target: { value: "0.001" } });
  fireEvent.click(screen.getByRole("button", { name: "保存" }));
  expect(save.mock.calls[0][0].trafficQuotaBytes).toBe(1073742n);
  fireEvent.change(screen.getByLabelText(/配额单位 quota/), { target: { value: "GB" } });
  fireEvent.click(screen.getByRole("button", { name: "保存" }));
  expect(save.mock.calls[1][0].trafficQuotaBytes).toBe(1000000n);
});

it("blocks tiny positive values and out-of-range quotas", () => {
  const save = editor();
  for (const value of ["0.00000000001", "4294967296"]) {
    fireEvent.change(screen.getByLabelText(/流量配额 quota/), { target: { value } });
    expect((screen.getByRole("button", { name: "保存" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("alert").textContent).toContain("配额无效");
  }
  expect(save).not.toHaveBeenCalled();
});
