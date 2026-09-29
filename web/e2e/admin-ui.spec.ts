import { expect, test, type Page } from "@playwright/test";

async function rpc(page: Page, method: string, body: unknown = {}) {
  return page.evaluate(async ({ method, body }) => {
    const response = await fetch('/heron.v1.AdminService/' + method, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const result = await response.json();
    if (!response.ok) throw new Error(method + ': ' + JSON.stringify(result));
    return result;
  }, { method, body });
}

test('后台明暗、双栈、编辑与计费、移动导航和键盘交互', async ({ page, browserName }, testInfo) => {
  const ids: string[] = [];
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  try {
    for (const [index, name] of ['tokyo-edge', 'seattle-core', 'frankfurt-worker'].entries()) {
      const result = await rpc(page, 'CreateNode', { name: `${name}-${browserName}` });
      const id = result.node.id;
      ids.push(id);
      await rpc(page, 'UpdateNode', { id, name: `${name}-${browserName}`, note: '生产节点 / 核心业务', public: true, trafficResetDay: 1, offlineGraceS: 0, countryPin: ['JP', 'US', 'DE'][index], tags: ['production', index === 0 ? 'edge' : 'compute'], billing: { price: String(12 + index * 8), currency: 'USD', billingCycle: 'BILLING_CYCLE_MONTHLY', expiresOn: '2027-10-01' } });
      await page.evaluate(async ({ token, index }) => {
        const checkedAt = String(Math.floor(Date.now() / 1000));
        const response = await fetch('/heron.v1.AgentService/Report', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token }, body: JSON.stringify({
          factsHash: '1', metrics: { bootId: 'browser-test', cpuPct: 12 + index * 15, memUsed: '536870912', memTotal: '2147483648', diskUsed: '2147483648', diskTotal: '21474836480', load1: 0.3, load5: 0.2, load15: 0.1, netRxBps: '524288', netTxBps: '131072' },
          facts: { hostname: 'edge.internal', agentVersion: 'dev', network: {
            ipv4: { state: index === 2 ? 'ADDRESS_DETECTION_STATE_FAILED' : 'ADDRESS_DETECTION_STATE_AVAILABLE', address: index === 2 ? '' : ['8.8.8.8', '1.1.1.1'][index], checkedAt },
            ipv6: index === 1 ? { state: 'ADDRESS_DETECTION_STATE_UNSUPPORTED', checkedAt } : { state: 'ADDRESS_DETECTION_STATE_AVAILABLE', address: '2606:4700:4700::1111', checkedAt },
          } },
        }) });
        if (!response.ok) throw new Error(await response.text());
      }, { token: result.token, index });
    }
    await page.goto('/admin/nodes');
    await page.getByLabel('后台配色').selectOption('dark');
    await page.setViewportSize({ width: 1440, height: 960 });
    await expect(page.getByText('2606:4700:4700::1111').first()).toBeVisible();
    await expect(page.getByText('不支持', { exact: true })).toBeVisible();
    await expect(page.getByText('探测失败', { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440);
    await page.screenshot({ path: testInfo.outputPath('nodes-dark-desktop.png'), fullPage: true });
    const edit = page.getByRole('button', { name: `编辑 tokyo-edge-${browserName}（#${ids[0]}）`, exact: true });
    await edit.click();
    const dialog = page.getByRole('dialog');
    const name = dialog.getByLabel(`名称 tokyo-edge-${browserName}（#${ids[0]}）`, { exact: true });
    await expect(name).toBeFocused();
    await name.fill('tokyo-renamed');
    await page.screenshot({ path: testInfo.outputPath('node-editor-desktop.png'), fullPage: true });
    for (let i = 0; i < 22; i++) {
      await page.keyboard.press('Tab');
      // 原生 dialog 允许 Tab 进入浏览器工具栏，但不能进入背景页面的控件。
      expect(await page.evaluate(() => document.activeElement === document.body || !!document.activeElement?.closest('dialog'))).toBe(true);
    }
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole('link', { name: `tokyo-renamed（#${ids[0]}）` })).toBeVisible();
    await page.getByRole('button', { name: `计费 tokyo-renamed（#${ids[0]}）` }).click();
    await dialog.getByLabel(`价格 tokyo-renamed（#${ids[0]}）`).fill('29.50');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText('USD 29.50 / 月')).toBeVisible();
    const renamedEdit = page.getByRole('button', { name: `编辑 tokyo-renamed（#${ids[0]}）` });
    await renamedEdit.click();
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(renamedEdit).toBeFocused();
    await page.getByLabel('后台配色').selectOption('light');
    await page.screenshot({ path: testInfo.outputPath('nodes-light-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('nodes-mobile.png'), fullPage: true });
    await renamedEdit.click();
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('node-editor-mobile.png'), fullPage: true });
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: '打开导航' }).click();
    await dialog.getByRole('link', { name: '总览', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole('heading', { name: '总览', exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('overview-mobile.png'), fullPage: true });
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.getByLabel('后台配色').selectOption('dark');
    await page.screenshot({ path: testInfo.outputPath('overview-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    for (const [route, heading] of [
      ['probes', '探测任务'], ['alerts', '告警规则'], ['events', '告警事件'], ['channels', '通知渠道'],
      ['appearance', '外观'], ['themes', '主题'], ['storage', '存储'], ['security', '安全'],
      ['security/credentials', '账户安全'], ['tokens', 'API token'], ['register', '注册窗口'],
    ]) {
      await page.goto('/admin/' + route);
      await expect(page.getByRole('heading', { name: heading, exact: true, level: 1 })).toBeVisible();
      await expect(page.getByText('加载中…', { exact: true })).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth), route).toBe(375);
      await page.screenshot({ path: testInfo.outputPath(route.replace('/', '-') + '-mobile.png'), fullPage: true });
    }
  } finally {
    for (const id of ids) await rpc(page, 'DeleteNode', { id });
  }
});
