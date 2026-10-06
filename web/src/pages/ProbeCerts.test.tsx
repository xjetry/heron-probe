import { expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { PinCapability, type SaveProbeTaskRequest } from "../gen/heron/v1/admin_pb";
import { PresentedReason } from "../gen/heron/v1/types_pb";
import { renderWithAdmin } from "../test/harness";
import { ProbeCerts } from "./ProbeCerts";

const pinA = Uint8Array.from({ length: 32 }, () => 1);
const pinB = Uint8Array.from({ length: 32 }, () => 2);
const config = Uint8Array.from({ length: 16 }, () => 9);
const routes = [{ path: "/probes/:id/certs", Component: ProbeCerts }];

const list = {
  configId: config,
  certSpkiSha256: new Uint8Array(),
  nodes: [
    { nodeId: 1n, pinCapability: PinCapability.UNSUPPORTED, unbound: { notAfterS: 1_800_000_000n, observedAt: 1_700_000_000n } },
    { nodeId: 2n, pinCapability: PinCapability.SUPPORTED, candidate: { spkiSha256: pinA, notAfterS: 1_900_000_000n, reason: PresentedReason.PIN_MISMATCH, observedAt: 1_700_000_100n } },
    { nodeId: 3n, pinCapability: PinCapability.UNKNOWN, candidate: { spkiSha256: pinB, notAfterS: 1_900_000_000n, reason: PresentedReason.CA_VERIFY_FAILED, observedAt: 1_700_000_200n } },
  ],
};

it("未下发与未绑定观测只展示，不一致的指纹逐个信任，成功后重新读取", async () => {
  const saved: SaveProbeTaskRequest[] = [];
  let reads = 0;
  renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 1n, name: "旧节点" }, { id: 2n, name: "甲" }, { id: 3n, name: "乙" }] }),
    listProbeCertificates: async () => { reads += 1; return list; },
    saveProbeTask: async (req) => { saved.push(req); return {}; },
  }, routes, "/probes/4/certs");
  expect(await screen.findByText(/任务照常下发，钉住后不再下发/)).toBeInTheDocument();
  expect(screen.getByText(/本次 hub 启动后尚未上报/)).toBeInTheDocument();
  const observed = new Date(1_800_000_000 * 1000);
  const stamp = screen.getByText(observed.toLocaleString());
  expect(stamp.tagName).toBe("TIME");
  expect(stamp).toHaveAttribute("dateTime", observed.toISOString());
  expect(screen.getByText(/未绑定配置身份/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /一键|全部信任/ })).toBeNull();
  const buttons = screen.getAllByRole("button", { name: "信任此指纹" });
  expect(buttons).toHaveLength(2);
  fireEvent.click(buttons[0]);
  await waitFor(() => expect(saved).toHaveLength(1));
  expect(saved[0].task?.id).toBe(4n);
  expect(saved[0].certPin?.action.case).toBe("setSpkiSha256");
  if (saved[0].certPin?.action.case === "setSpkiSha256") expect(saved[0].certPin.action.value).toEqual(pinA);
  expect(saved[0].expectedConfigId).toEqual(config);
  await waitFor(() => expect(reads).toBeGreaterThan(1));
  expect(screen.getByRole("alert").textContent).toMatch(/已重新读取/);
  expect(screen.getByRole("alert").textContent).not.toMatch(/请刷新/);
});

it("钉住的任务对不支持的 agent 写明没有下发", async () => {
  renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 1n, name: "旧节点" }] }),
    listProbeCertificates: async () => ({ ...list, certSpkiSha256: pinA, nodes: [list.nodes[0]] }),
  }, routes, "/probes/4/certs");
  expect(await screen.findByText("能力：agent 不支持钉指纹、任务没有下发到它")).toBeInTheDocument();
});

it("前置条件失败时重新读取且不再提交", async () => {
  let calls = 0;
  let reads = 0;
  renderWithAdmin({
    listNodes: async () => ({ nodes: [{ id: 2n, name: "甲" }] }),
    listProbeCertificates: async () => { reads += 1; return { configId: config, nodes: [list.nodes[1]] }; },
    saveProbeTask: async () => { calls += 1; throw new ConnectError("expected_config_id does not match", Code.FailedPrecondition); },
  }, routes, "/probes/4/certs");
  fireEvent.click(await screen.findByRole("button", { name: "信任此指纹" }));
  expect(await screen.findByText(/已重新读取/)).toBeInTheDocument();
  expect(screen.getByRole("alert").textContent).not.toMatch(/请刷新/);
  await waitFor(() => expect(reads).toBeGreaterThan(1));
  expect(calls).toBe(1);
});
