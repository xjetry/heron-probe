import { expect, test, type Page, type Request } from "@playwright/test";
import { READ_DEADLINE_MESSAGE } from "../src/api/deadline";

async function rpc(page: Page, method: string, body: unknown = {}) {
  const response = await page.evaluate(async ({ method, body }) => {
    const response = await fetch(`/heron.v1.AdminService/${method}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    return { status: response.status, body: await response.json() };
  }, { method, body });
  expect(response.status, `${method}: ${JSON.stringify(response.body)}`).toBe(200);
  return response.body;
}

function windowOf(request: Request) {
  return request.method() === "GET" ? JSON.parse(new URL(request.url()).searchParams.get("message")!) : request.postDataJSON();
}

for (const hours of [8, -8]) for (const [service, comparison] of [
  ["AdminService", false], ["PublicService", false], ["AdminService", true], ["PublicService", true],
] as const) test(`${service} ${comparison ? "对比" : "节点"} 浏览器偏移 ${hours} 小时仍使用 hub 窗口`, async ({ page, browserName }, testInfo) => {
  await page.goto("/admin/login");
  await rpc(page, "Login", { password: "local-browser-test-password" });
  // 公开端用例的前提是公开页总闸打开；它是 hub 共用设置，不能依赖之前的 spec 留下的状态。
  if (service === "PublicService") await rpc(page, "UpdateSettings", { settings: { publicEnabled: true } });
  const { node } = await rpc(page, "CreateNode", { name: `clock-${browserName}` });
  let taskId: string | undefined;
  try {
    await rpc(page, "UpdateNode", { id: node.id, name: node.name, public: true, trafficResetDay: 1, offlineGraceS: 0 });
    const saved = await rpc(page, "SaveProbeTask", {
      task: { kind: "PROBE_KIND_TCP", target: "127.0.0.1:18987", intervalS: 60, timeoutMs: 1000 }, nodeIds: [node.id],
    });
    taskId = saved.task.task.id;
    const { now } = await rpc(page, "GetSnapshot");
    await page.clock.setFixedTime((Number(now) + hours * 3600) * 1000);
    const admin = service === "AdminService";
    const path = comparison ? (admin ? `/admin/probes/${taskId}/compare` : `/probes/${taskId}`) : `${admin ? "/admin" : ""}/nodes/${node.id}`;
    const methods = comparison ? ["QueryProbeComparison"] : ["QueryMetrics", "QueryProbes"];
    const requests = methods.map((method) => page.waitForRequest((request) => request.url().includes(`/heron.v1.${service}/${method}`)));
    await page.goto(path);
    for (const request of await Promise.all(requests)) {
      const window = windowOf(request);
      expect(Number(window.to) - Number(window.from)).toBe(86400);
      expect(Math.abs(Number(window.to) - Number(now))).toBeLessThanOrEqual(120);
      expect(Math.abs(Number(window.to) - (Number(now) + hours * 3600))).toBeGreaterThan(28_000);
      expect(Number(window.to) % 60).toBe(0);
      expect(request.headers()["connect-timeout-ms"]).toBe("30000");
    }
    await expect(page.getByRole("navigation", { name: "时间窗口" })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("window-desktop.png"), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    await page.screenshot({ path: testInfo.outputPath("window-mobile.png"), fullPage: true });
  } finally {
    if (taskId) await rpc(page, "DeleteProbeTask", { id: taskId });
    await rpc(page, "DeleteNode", { id: node.id });
  }
});

for (const [path, service] of [["/admin/", "AdminService"], ["/", "PublicService"]] as const) {
  test(`${service} 挂住轮询在预算内显示横幅，解除拦截后恢复`, async ({ page }) => {
    await page.goto("/admin/login");
    await rpc(page, "Login", { password: "local-browser-test-password" });
    if (service === "PublicService") await rpc(page, "UpdateSettings", { settings: { publicEnabled: true } });
    // install 接管 setTimeout；pauseAt 停住自动走时，runFor 逐段执行真实页面的截止与重试计时器。
    const start = Date.now();
    await page.clock.install({ time: start });
    await page.clock.pauseAt(start);
    const initial = page.waitForResponse((response) => response.url().includes(`/heron.v1.${service}/GetSnapshot`) && response.ok());
    await page.goto(path);
    await initial;
    await page.clock.runFor(10);
    let calls = 0;
    let hanging = true;
    const pattern = `**/heron.v1.${service}/GetSnapshot**`;
    await page.route(pattern, async (route) => {
      calls += 1;
      if (!hanging) await route.continue();
    });
    await page.clock.runFor(1990);
    await expect.poll(() => calls).toBe(1);
    await page.clock.runFor(30_000);
    await page.clock.runFor(1000);
    await expect.poll(() => calls).toBe(2);
    await page.clock.runFor(30_000);
    await page.clock.runFor(2000);
    await expect.poll(() => calls).toBe(3);
    await page.clock.runFor(29_999);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.clock.runFor(1);
    // 最终错误的订阅通知由 TanStack 排入下一轮任务；不是额外的网络等待预算。
    await page.clock.runFor(1);
    await expect(page.getByRole("alert")).toContainText(READ_DEADLINE_MESSAGE);
    hanging = false;
    const recovered = page.waitForResponse((response) => response.url().includes(`/heron.v1.${service}/GetSnapshot`) && response.ok());
    await page.clock.runFor(2000);
    await recovered;
    await page.clock.runFor(1);
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(calls).toBe(4);
    await page.unroute(pattern);
  });
}
