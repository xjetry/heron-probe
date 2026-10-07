import { expect, test, type Page } from "@playwright/test";

async function rpc(page: Page, service: string, method: string, body: unknown, token = "") {
  return page.evaluate(async ({ service, method, body, token }) => {
    const response = await fetch(`/heron.v1.${service}/${method}`, {
      method: "POST", credentials: token ? "omit" : "same-origin",
      headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify(body),
    });
    return { status: response.status, body: await response.json() };
  }, { service, method, body, token });
}

test("采集诊断从真实上报进入管理详情且不进入公开页", async ({ page, browserName }, testInfo) => {
  await page.goto("/admin/login");
  expect((await rpc(page, "AdminService", "Login", { password: "local-browser-test-password" })).status).toBe(200);
  const created = await rpc(page, "AdminService", "CreateNode", { name: `agent-health-${browserName}` });
  expect(created.status).toBe(200);
  const { node } = created.body;
  try {
    const registered = await rpc(page, "AgentService", "Register", { key: created.body.token });
    expect(registered.status).toBe(200);
    const { token } = registered.body;
    const updated = await rpc(page, "AdminService", "UpdateNode", { id: node.id, name: node.name, public: true, trafficResetDay: 1, offlineGraceS: 0 });
    expect(updated.status, JSON.stringify(updated.body)).toBe(200);
    await page.goto(`/admin/nodes/${node.id}`);
    await page.getByRole("tab", { name: "Agent 诊断", exact: true }).click();
    const card = page.getByRole("region", { name: "Agent 运行诊断" });
    await expect(card.getByText("Agent 未提供诊断信息，请更新 Agent 后等待上报。")).toBeVisible();
    const diagnostics = {
      netInclude: ["private-uplink-*"], netInterfaces: ["private-uplink-0"], netInterfacesTotal: 1,
      failedCollectors: ["COLLECTION_COMPONENT_DISK"], reportIntervalMs: 10000,
    };
    const report = await rpc(page, "AgentService", "Report", {
      metrics: { bootId: "browser-boot", netCounterEpoch: "a".repeat(64), netRxTotal: "100", netTxTotal: "200" },
      factsHash: "101", facts: { agentVersion: "browser-diagnostic-version", diagnostics },
    }, token);
    expect(report.status).toBe(200);
    await expect(card.getByText("private-uplink-0", { exact: true })).toBeVisible({ timeout: 15000 });
    await expect(card.getByText("磁盘", { exact: true })).toBeVisible();
    await expect(card.getByText("10000 ms", { exact: false })).toBeVisible();
    await expect(card.getByText("最近保存的诊断，不保证实时健康。生效参数只读。")).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("diagnostics-desktop.png"), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath("diagnostics-mobile.png"), fullPage: true });
    expect((await rpc(page, "AgentService", "Report", {
      metrics: { bootId: "browser-boot", netCounterEpoch: "a".repeat(64) },
      factsHash: "102", facts: { diagnostics: { ...diagnostics, failedCollectors: [] } },
    }, token)).status).toBe(200);
    await expect(card.getByText("最近采集未报告失败", { exact: true })).toBeVisible({ timeout: 15000 });
    await page.goto(`/admin/nodes/${node.id}?tab=diagnostics`);
    await expect(card).toBeVisible();
    const snapshot = await rpc(page, "PublicService", "GetSnapshot", {});
    expect(snapshot.status).toBe(200);
    expect(snapshot.body.nodes.some((entry: { id: string }) => entry.id === node.id)).toBe(true);
    const json = JSON.stringify(snapshot.body);
    for (const value of ["private-uplink", "diagnostics", "netCounterEpoch", token]) expect(json).not.toContain(value);
  } finally {
    await rpc(page, "AdminService", "DeleteNode", { id: node.id });
  }
});
