import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// 设计 §6 的公开页禁止字段：任何一个出现在页面文字里都算失败。
const FORBIDDEN = ["可用率", "在线率", "SLA", "宕机", "不可达", "主机名", "内核", "agent 版本", "IPv4", "IPv6", "事件", "告警", "刷新"];

// e2e 的 hub 是每次新建的库，公开页总闸默认关闭（关闸时连 SPA 的静态资源都是 404），先登录打开；RPC 再由 route 拦截。
async function setPublicEnabled(page: Page, enabled: boolean) {
  await page.goto("/admin/login");
  await page.evaluate(async (enabled) => {
    const call = async (method: string, body: unknown) => {
      const response = await fetch("/heron.v1.AdminService/" + method, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(method + ": " + await response.text());
    };
    await call("Login", { password: "local-browser-test-password" });
    await call("UpdateSettings", { settings: { publicEnabled: enabled } });
  }, enabled);
}

test("公开总览：状态墙、详情、卡片、手机列表与数据边界", async ({ page }, testInfo) => {
  await setPublicEnabled(page, true);
  const now = Math.floor(Date.now() / 1000);
  const minute = now - now % 60;
  const nodes = [
    { id: "1", name: "tokyo-core", country: "JP", online: true, tags: ["机房"], lastSeenAt: String(now - 2), facts: { os: "Debian 13", arch: "amd64", virtualization: "kvm", cpuModel: "EPYC", cpuCores: 4 }, metrics: { cpuPct: 72, memUsed: String(3 * 1024 ** 3), memTotal: String(4 * 1024 ** 3), diskUsed: String(8 * 1024 ** 3), diskTotal: String(32 * 1024 ** 3), uptimeS: "720000", load1: 0.3, load5: 0.2, load15: 0.1, netRxBps: String(128 * 1024), netTxBps: String(32 * 1024), tcpConns: 20, udpConns: 2, procs: 120 }, traffic: { periodRx: String(12 * 1024 ** 3), periodTx: String(8 * 1024 ** 3) }, billing: { price: "12", currency: "USD", billingCycle: "BILLING_CYCLE_MONTHLY", expiresOn: "2027-10-01", daysLeft: 25 } },
    { id: "2", name: "tokyo-home", country: "JP", online: false, tags: ["家宽"], lastSeenAt: String(now - 7200), metrics: { cpuPct: 5 } },
    { id: "3", name: "hk-edge", country: "HK", online: true, tags: ["机房"], lastSeenAt: String(now - 1), maintenance: true, metrics: { cpuPct: 10, memUsed: String(1024 ** 3), memTotal: String(4 * 1024 ** 3) } },
    { id: "4", name: "香港家宽-超长节点名称用于验证窄屏布局与完整可访问名称", country: "HK", online: true, tags: ["家宽"], lastSeenAt: String(now - 1), metrics: { cpuPct: 95, memUsed: String(Math.floor(3.9 * 1024 ** 3)), memTotal: String(4 * 1024 ** 3) } },
    { id: "5", name: "等待首次接入", country: "", online: false, tags: [] },
  ];
  await page.route("**/heron.v1.PublicService/GetSite**", (route) => route.fulfill({ json: { title: "Heron · 基础设施", theme: "dark", adminPath: "/admin/" } }));
  await page.route("**/heron.v1.PublicService/GetSnapshot**", (route) => route.fulfill({ json: { now: String(now), nodes, tags: ["家宽", "机房"] } }));
  await page.route("**/heron.v1.PublicService/QueryMetrics**", (route) => {
    const request = JSON.parse(new URL(route.request().url()).searchParams.get("message") ?? "{}");
    return route.fulfill({ json: { level: "1m", stepS: 60, ts: [String(minute - 120), String(minute - 60)], series: request.nodeId === "1" ? [{ name: "rx_bytes", unit: "bytes", samples: [{ n: 1, sum: "6000" }, { n: 1, sum: "3000" }] }, { name: "tx_bytes", unit: "bytes", samples: [{ n: 1, sum: "600" }, { n: 1, sum: "300" }] }] : [] } });
  });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto("/");

  // 顶栏只有标题、实时说明、明暗切换、登录；没有导航。
  const header = page.locator("header.public-header");
  await expect(header.getByRole("link")).toHaveText(["Heron · 基础设施", "登录"]);
  await expect(header.getByText("实时 · 每 2 秒")).toBeVisible();
  await expect(page.getByRole("navigation")).toHaveCount(0);
  await expect(page.locator("footer.site-footer")).toHaveCount(0);

  // 汇总与分组：维护中不算在线；组按在线数降序，未知最后。
  await expect(page.getByText("2 / 5 在线")).toBeVisible();
  // 日本与香港都是 1 / 2：同在线数、同总数时按代码排，HK 在 JP 前。
  await expect(page.locator(".wall-group > summary")).toHaveText(["香港 · 1 / 2 在线", "日本 · 1 / 2 在线", "未知 · 0 / 1 在线"]);
  await expect(page.locator(".tile[data-status='offline']").getByText("离线 · 2 小时前")).toBeVisible();

  // 详情面板默认选中第一个节点；点另一个方块只切换，不导航。
  const panel = page.getByRole("complementary", { name: "节点详情" });
  await expect(panel.getByRole("heading", { name: "hk-edge" })).toBeVisible();
  await page.getByRole("link", { name: "tokyo-core", exact: true }).click();
  await expect(panel.getByRole("heading", { name: "tokyo-core" })).toBeVisible();
  await expect(panel.getByRole("img", { name: "最近 1 小时网络速率", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "hk-edge" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(panel.getByRole("heading", { name: "hk-edge" })).toBeVisible();
  await expect(panel.getByText("维护中 · 最近上报 刚刚")).toBeVisible();
  await expect(panel.getByRole("img", { name: "最近 1 小时网络速率：无读数", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440);
  await page.screenshot({ path: testInfo.outputPath("wall-dark.png"), fullPage: true });

  // 筛选：地区下拉 + 标签下拉 + 只看在线。
  await page.getByRole("group", { name: "地区" }).getByRole("button", { name: /^地区/ }).click();
  await page.getByRole("checkbox", { name: "日本" }).check();
  await page.getByRole("checkbox", { name: "日本" }).press("Escape");
  await expect(page.locator(".summary-count")).toHaveText("1 / 2 在线");
  await page.getByRole("button", { name: "只看在线" }).click();
  await expect(page.locator(".tile")).toHaveCount(1);
  await page.getByRole("button", { name: "移除 日本" }).click();
  await page.getByRole("button", { name: "只看在线" }).click();

  // 卡片视图：只有在线与维护中出卡片，离线折叠；卡片有费用与到期。
  await page.getByRole("group", { name: "视图" }).getByRole("button", { name: "卡片" }).click();
  await expect(page.getByRole("article")).toHaveCount(3);
  const card = page.getByRole("article", { name: "tokyo-core" });
  await expect(card.getByText("US$12 / 月")).toBeVisible();
  await expect(card.locator(".expiry")).toHaveAttribute("data-level", "attention");
  await expect(card.locator(".expiry")).toContainText("剩 25 天");
  const folded = page.locator("details.folded-nodes");
  await expect(folded.locator("summary")).toHaveText("离线与从未上报 · 2");
  await folded.locator("summary").click();
  await expect(folded.getByRole("row")).toHaveCount(3);
  await page.screenshot({ path: testInfo.outputPath("cards-dark.png"), fullPage: true });

  // 浅色：访客切换压过站点的 dark。
  await page.getByRole("button", { name: "明暗切换" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.screenshot({ path: testInfo.outputPath("cards-light.png"), fullPage: true });

  // 数据边界：页面文字里没有禁止字段。
  const text = await page.evaluate(() => document.body.innerText);
  for (const word of FORBIDDEN) expect(text, word).not.toContain(word);

  // 手机：墙是单列列表，点行直接进节点页，没有横向溢出。
  await page.getByRole("group", { name: "视图" }).getByRole("button", { name: "状态墙" }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(panel).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.screenshot({ path: testInfo.outputPath("wall-mobile.png"), fullPage: true });
  await page.getByRole("link", { name: nodes[3].name }).click();
  await expect(page).toHaveURL(/\/nodes\/4$/);
});

test("公开页关闭时分享链接得到说明页而不是 404", async ({ page, hub }) => {
  // 总闸是整个 hub 共用的设置，后续 spec 共用同一个 hub：关闸只在本用例内有效，收尾时恢复（fixtures.ts）。
  hub.atEnd("恢复公开页总闸", () => hub.rpc("UpdateSettings", { settings: { publicEnabled: true } }));
  await setPublicEnabled(page, false);
  const response = await page.goto("/nodes/7");
  expect(response?.status()).toBe(200);
  await expect(page.getByRole("heading", { name: "公开页已关闭" })).toBeVisible();
  await expect(page.getByRole("link", { name: "管理员登录" })).toHaveAttribute("href", "/admin/");
});
