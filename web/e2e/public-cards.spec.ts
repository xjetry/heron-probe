import { expect, test } from "@playwright/test";

test("公开卡片明暗、多节点、缺失读数、长名称和键盘详情入口", async ({ page }, testInfo) => {
  let theme = "dark";
  const now = Math.floor(Date.now() / 1000);
  const names = ["seattle-core", "tokyo-residential", "hongkong-edge", "frankfurt-worker", "singapore-storage", "香港家宽-超长节点名称用于验证窄屏布局与完整可访问名称", "osaka-offline", "等待首次接入"];
  const nodes = names.map((name, index) => ({
    id: String(index + 1), name, country: ["US", "JP", "HK", "DE", "SG", "HK", "JP", ""][index],
    online: index < 6, tags: [index === 1 || index === 5 ? "家宽" : "机房"],
    ...(index === 7 ? {} : {
      lastSeenAt: String(now - (index === 6 ? 7200 : 2)),
      facts: { os: index === 5 ? "Debian GNU/Linux 13 (trixie)" : "Debian 13", arch: "amd64", virtualization: "kvm", cpuCores: index % 2 ? 2 : 4 },
      metrics: { cpuPct: [3.5, 0, 21, 8.2, 87, 12, 42][index], memUsed: String(512 * 1024 ** 2 + index * 256 * 1024 ** 2), memTotal: String(4 * 1024 ** 3), diskUsed: String((index + 1) * 1024 ** 3), diskTotal: String(32 * 1024 ** 3), uptimeS: String(720000 + index * 1000), load1: 0.02, load5: 0.05, load15: 0, netRxBps: String(index * 128 * 1024), netTxBps: String(index * 32 * 1024) },
    }),
    traffic: { periodRx: String(index === 7 ? 0 : index * 12 * 1024 ** 3), periodTx: String(index === 7 ? 0 : index * 8 * 1024 ** 3) },
    ...(index === 7 ? {} : { billing: { price: String(12 + index * 5), currency: "USD", billingCycle: "BILLING_CYCLE_MONTHLY", expiresOn: "2027-10-01", daysLeft: [64, 259, 7, 28, 1, -3, 12][index] } }),
  }));
  // 固定公开快照隔离时钟和网络波动，布局验收同时覆盖在线、离线和从未上报。
  await page.route("**/heron.v1.PublicService/GetSite**", route => route.fulfill({ json: { title: "Heron · 基础设施", theme } }));
  await page.route("**/heron.v1.PublicService/GetSnapshot**", route => route.fulfill({ json: { now: String(now), nodes, tags: ["家宽", "机房"] } }));
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1600, height: 1100 });
  await page.goto("/");
  await expect(page.getByRole("article")).toHaveCount(8);
  const stale = page.getByRole("article", { name: "osaka-offline" });
  await expect(stale.getByText("最后读数")).toBeVisible();
  await expect(stale.getByRole("meter", { name: "42%" })).toBeVisible();
  await expect(page.getByRole("article", { name: "等待首次接入" }).getByRole("meter")).toHaveCount(0);
  await expect(page.getByRole("article", { name: "tokyo-residential" }).getByRole("meter", { name: "0.0%" })).toHaveAttribute("aria-valuenow", "0");
  for (const mode of ["dark", "light"]) {
    theme = mode;
    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-theme", mode);
    await expect(page.getByRole("article")).toHaveCount(8);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1600);
    const boxes = await page.getByRole("article").evaluateAll(cards => cards.map(card => ({ x: card.getBoundingClientRect().x, y: card.getBoundingClientRect().y })));
    expect(new Set(boxes.slice(0, 4).map(box => box.y)).size).toBe(1);
    expect(boxes[4].y).toBeGreaterThan(boxes[0].y);
    await page.screenshot({ path: testInfo.outputPath(`public-cards-${mode}.png`), fullPage: true });
  }
  for (const width of [375, 320]) {
    await page.setViewportSize({ width, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
    const long = page.getByRole("article", { name: names[5] });
    await long.scrollIntoViewIfNeeded();
    await expect(long.getByRole("link", { name: names[5], exact: true })).toHaveAttribute("title", names[5]);
    expect(await long.evaluate(card => card.scrollWidth <= card.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`public-cards-mobile-${width}.png`), fullPage: true });
  }
  const firstLink = page.getByRole("link", { name: names[0], exact: true });
  await firstLink.focus();
  await expect(firstLink).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/nodes\/1$/);
  await page.goBack();
  await expect(page.getByRole("article")).toHaveCount(8);
  const meter = page.getByRole("article", { name: names[0] }).getByRole("meter").first();
  await meter.scrollIntoViewIfNeeded();
  const box = await meter.boundingBox();
  if (!box) throw new Error("资源条不可见");
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  await expect(page).toHaveURL(/\/nodes\/1$/);
});
