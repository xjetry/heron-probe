import { expect, test, type Page } from "@playwright/test";

async function rpc(page: Page, method: string, body: unknown = {}) {
  return page.evaluate(async ({ method, body }) => {
    const response = await fetch('/heron.v1.AdminService/' + method, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const result = await response.json();
    if (!response.ok) throw new Error(method + ': ' + JSON.stringify(result));
    return result;
  }, { method, body });
}

test('注册命令复制与移动端布局', async ({ page, context, browserName }, testInfo) => {
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  await page.goto('/admin/register');
  await page.getByRole('button', { name: '开启新窗口' }).click();
  await expect(page.getByLabel('curl 安装命令')).toBeVisible();
  if (browserName === 'chromium') {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    for (const tool of ['curl', 'wget']) {
      const command = await page.getByLabel(`${tool} 安装命令`).textContent();
      await page.getByRole('button', { name: `复制 ${tool} 命令` }).click();
      await expect(page.getByRole('button', { name: `复制 ${tool} 命令` })).toHaveText('已复制');
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command);
    }
  }
  await page.screenshot({ path: testInfo.outputPath('register-copy-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
  await expect(page.getByRole('button', { name: '复制 curl 命令' })).toBeVisible();
  await expect(page.getByRole('button', { name: '复制 wget 命令' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('register-copy-mobile.png'), fullPage: true });
  await page.getByRole('button', { name: '关闭窗口' }).click();
  await expect(page.getByRole('button', { name: '复制 curl 命令' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '复制 wget 命令' })).toHaveCount(0);
});

test('后台明暗、双栈、编辑与计费、移动导航和键盘交互', async ({ page, context, browserName }, testInfo) => {
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
      await page.evaluate(async ({ key, index }) => {
        const registered = await fetch('/heron.v1.AgentService/Register', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ key }) });
        if (!registered.ok) throw new Error(await registered.text());
        const { token } = await registered.json();
        const checkedAt = String(Math.floor(Date.now() / 1000));
        const response = await fetch('/heron.v1.AgentService/Report', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token }, body: JSON.stringify({
          factsHash: '1', metrics: { bootId: 'browser-test', cpuPct: 12 + index * 15, memUsed: '536870912', memTotal: '2147483648', diskUsed: '2147483648', diskTotal: '21474836480', load1: 0.3, load5: 0.2, load15: 0.1, netRxBps: '524288', netTxBps: '131072' },
          facts: { hostname: 'edge.internal', agentVersion: 'dev', network: {
            ipv4: { state: index === 2 ? 'ADDRESS_DETECTION_STATE_FAILED' : 'ADDRESS_DETECTION_STATE_AVAILABLE', address: index === 2 ? '' : ['8.8.8.8', '1.1.1.1'][index], checkedAt },
            ipv6: index === 1 ? { state: 'ADDRESS_DETECTION_STATE_UNSUPPORTED', checkedAt } : { state: 'ADDRESS_DETECTION_STATE_AVAILABLE', address: '2606:4700:4700::1111', checkedAt },
          } },
        }) });
        if (!response.ok) throw new Error(await response.text());
      }, { key: result.token, index });
    }
    await page.goto('/admin/nodes');
    await page.getByLabel('后台配色').selectOption('dark');
    await page.setViewportSize({ width: 1440, height: 960 });
    await expect(page.getByText('2606:4700:4700::1111').first()).toBeVisible();
    await expect(page.getByText('不支持', { exact: true })).toBeVisible();
    await expect(page.getByText('探测失败', { exact: true })).toBeVisible();
    const copy4 = page.getByRole('button', { name: '复制 IPv4 8.8.8.8', exact: true });
    const copy6 = page.getByRole('button', { name: '复制 IPv6 2606:4700:4700::1111', exact: true }).first();
    await expect(copy4).toBeVisible();
    await expect(copy6).toBeVisible();
    if (browserName === 'chromium') {
      await context.grantPermissions(['clipboard-read', 'clipboard-write']);
      for (const [button, address] of [[copy4, '8.8.8.8'], [copy6, '2606:4700:4700::1111']] as const) {
        await button.click();
        await expect(button).toHaveAttribute('title', '已复制');
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(address);
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440);
    await page.screenshot({ path: testInfo.outputPath('nodes-dark-desktop.png'), fullPage: true });
    const edit = page.getByRole('button', { name: `编辑 tokyo-edge-${browserName}（#${ids[0]}）`, exact: true });
    await edit.click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('button', { name: '复制 IPv4 8.8.8.8', exact: true })).toBeVisible();
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
    const expiryLabel = `到期日 tokyo-renamed（#${ids[0]}）`;
    const year = dialog.getByRole('textbox', { name: `${expiryLabel} 年`, exact: true });
    const month = dialog.getByRole('textbox', { name: `${expiryLabel} 月`, exact: true });
    const day = dialog.getByRole('textbox', { name: `${expiryLabel} 日`, exact: true });
    await year.fill('2031');
    await month.fill('02');
    await day.fill('29');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).toBeVisible();
    const unchanged = await rpc(page, 'ListNodes');
    expect(unchanged.nodes.find((node: { id: string }) => node.id === ids[0]).billing.expiresOn).toBe('2027-10-01');
    await year.fill('');
    await year.pressSequentially('2031');
    await expect(month).toBeFocused();
    await month.pressSequentially('12');
    await expect(day).toBeFocused();
    await day.pressSequentially('25');
    await expect(year).toHaveValue('2031');
    await expect(month).toHaveValue('12');
    await expect(day).toHaveValue('25');
    await page.screenshot({ path: testInfo.outputPath('billing-desktop.png'), fullPage: true });
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText('US$29.50 / 月')).toBeVisible();
    const updated = await rpc(page, 'ListNodes');
    expect(updated.nodes.find((node: { id: string }) => node.id === ids[0]).billing.expiresOn).toBe('2031-12-25');
    const renamedEdit = page.getByRole('button', { name: `编辑 tokyo-renamed（#${ids[0]}）` });
    await renamedEdit.click();
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await expect(renamedEdit).toBeFocused();
    await page.getByLabel('后台配色').selectOption('light');
    await page.screenshot({ path: testInfo.outputPath('nodes-light-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await expect(copy4).toBeVisible();
    await expect(copy6).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('nodes-mobile.png'), fullPage: true });
    await renamedEdit.click();
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('node-editor-mobile.png'), fullPage: true });
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: `计费 tokyo-renamed（#${ids[0]}）` }).click();
    await page.screenshot({ path: testInfo.outputPath('billing-mobile.png'), fullPage: true });
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
      ['appearance', '外观'], ['themes', '主题'], ['storage', '存储'], ['updates', '在线更新'], ['security', '安全'],
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

test('节点拖拽、键盘和移动端菜单保存同一完整顺序', async ({ page, browserName }, testInfo) => {
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  const ids: string[] = [];
  const label = (index: number) => `order-${index}-${browserName}（#${ids[index]}）`;
  const actual = async () => (await rpc(page, 'ListNodes')).nodes.filter((node: { id: string }) => ids.includes(node.id)).map((node: { id: string }) => node.id);
  try {
    for (let i = 0; i < 3; i++) ids.push((await rpc(page, 'CreateNode', { name: `order-${i}-${browserName}` })).node.id);
    await page.goto('/admin/nodes');
    await page.setViewportSize({ width: 1440, height: 960 });
    const handle = page.getByRole('button', { name: `调整顺序 ${label(0)}`, exact: true });
    const target = page.getByRole('link', { name: label(2), exact: true }).locator('xpath=ancestor::tr');
    await expect(handle).toBeEnabled();
    const box = await target.boundingBox();
    expect(box).not.toBeNull();
    await handle.dragTo(target, { targetPosition: { x: 30, y: box!.height - 5 } });
    await expect(page.getByText('顺序已保存', { exact: true })).toBeVisible();
    await expect.poll(actual).toEqual([ids[1], ids[2], ids[0]]);
    await handle.focus();
    await handle.press('Home');
    await expect.poll(actual).toEqual([ids[0], ids[1], ids[2]]);
    await expect(handle).toBeFocused();
    await handle.press('ArrowDown');
    await expect(handle).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(handle).toBeFocused();
    await expect.poll(actual).toEqual([ids[1], ids[2], ids[0]]);
    await handle.press('Home');
    await expect.poll(actual).toEqual([ids[0], ids[1], ids[2]]);
    await handle.press('End');
    await expect(handle).toBeFocused();
    await expect.poll(actual).toEqual([ids[1], ids[2], ids[0]]);
    await handle.press('Home');
    await expect.poll(actual).toEqual([ids[0], ids[1], ids[2]]);
    await page.screenshot({ path: testInfo.outputPath('node-order-desktop.png'), fullPage: true });
    await page.getByRole('searchbox', { name: '搜索节点' }).fill(`order-0-${browserName}`);
    await expect(handle).toBeDisabled();
    // 过滤时拖动与上下移仍禁用，但按全序名次的「移动到…」可用，菜单保持可用（不被禁用）。
    const filteredMenu = page.getByRole('combobox', { name: `移动 ${label(0)}`, exact: true });
    await expect(filteredMenu).toBeEnabled();
    await expect(page.getByText('搜索或按标签过滤时不能用拖动或上下移（它们保存完整排列）；可用「移动到…」按全序名次移动，或清空过滤后再调整。')).toBeVisible();
    await page.getByRole('searchbox', { name: '搜索节点' }).fill('');
    await page.setViewportSize({ width: 375, height: 812 });
    await page.getByRole('combobox', { name: `移动 ${label(0)}`, exact: true }).selectOption('last');
    await expect.poll(actual).toEqual([ids[1], ids[2], ids[0]]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('node-order-mobile.png'), fullPage: true });
  } finally {
    for (const id of ids) await rpc(page, 'DeleteNode', { id });
  }
});

test('节点按全序名次整体移动到指定位置', async ({ page, browserName }, testInfo) => {
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  const ids: string[] = [];
  const label = (index: number) => `move-${index}-${browserName}（#${ids[index]}）`;
  // ListNodes 返回的是 hub 的完整列表：本用例只断言自己那批节点的相对顺序，名次则对完整列表验证（1 起且与行序一致）。
  const hubList = async () => (await rpc(page, 'ListNodes')).nodes as { id: string; position: number }[];
  const mineInOrder = async () => (await hubList()).filter((node) => ids.includes(node.id)).map((node) => node.id);
  const positionsMatchList = async () => {
    const all = await hubList();
    return all.every((node, index) => node.position === index + 1);
  };
  try {
    for (let i = 0; i < 5; i++) ids.push((await rpc(page, 'CreateNode', { name: `move-${i}-${browserName}` })).node.id);
    await page.goto('/admin/nodes');
    await page.setViewportSize({ width: 1440, height: 960 });
    const total = (await hubList()).length;
    // 本批 5 个节点刚连着创建，占全序中连续的名次：从 ids[0] 的名次推出整批的基准位次。
    const base = (await hubList()).find((node) => node.id === ids[0])!.position;
    const handle = (index: number) => page.getByRole('button', { name: `调整顺序 ${label(index)}`, exact: true });
    // 多选 move-1、move-3（本批第 2、4 位）整体移到本批第 3 位：其余相对顺序不变。
    await page.getByRole('checkbox', { name: `选择 ${label(1)}`, exact: true }).check();
    await page.getByRole('checkbox', { name: `选择 ${label(3)}`, exact: true }).check();
    await expect(page.getByText('已选择 2 个节点')).toBeVisible();
    await page.getByRole('button', { name: '移动到…', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toHaveAccessibleName('移动节点');
    await expect(dialog.getByText(`共 ${total} 个节点，将移动其中的 2 个；其余节点相对顺序不变。`)).toBeVisible();
    const input = dialog.getByLabel(`目标位置（1–${total - 1}）`, { exact: true });
    await expect(input).toHaveAttribute('max', String(total - 1));
    await expect(dialog.getByText('将 2 个节点移到第 1–2 位')).toBeVisible();
    await input.fill(String(base + 2));
    await expect(dialog.getByText(`将 2 个节点移到第 ${base + 2}–${base + 3} 位`)).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('node-move-modal.png'), fullPage: true });
    await dialog.getByRole('button', { name: '移动', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText('已选择 0 个节点')).toBeVisible();
    await expect.poll(mineInOrder).toEqual([ids[0], ids[2], ids[1], ids[3], ids[4]]);
    expect(await positionsMatchList()).toBe(true);
    // 序号列跟着新的全序名次走。
    await expect(handle(0)).toHaveText(String(base));
    await expect(handle(2)).toHaveText(String(base + 1));
    await expect(handle(1)).toHaveText(String(base + 2));
    await expect(handle(3)).toHaveText(String(base + 3));
    // 过滤到单个节点：拖动禁用，但行菜单可按全序名次移动这一个节点。
    await page.getByRole('searchbox', { name: '搜索节点' }).fill(`move-4-${browserName}`);
    await expect(page.getByRole('link', { name: label(4), exact: true })).toBeVisible();
    const positionOfLast = (await hubList()).find((node) => node.id === ids[4])!.position;
    await expect(handle(4)).toHaveText(String(positionOfLast));
    await expect(handle(4)).toBeDisabled();
    await page.getByRole('combobox', { name: `移动 ${label(4)}`, exact: true }).selectOption('move');
    const single = page.getByRole('dialog');
    await expect(single).toHaveAccessibleName('移动节点');
    const singleInput = single.getByLabel(`目标位置（1–${total}）`, { exact: true });
    await expect(single.getByText('移到第 1 位')).toBeVisible();
    await singleInput.fill('2');
    await expect(single.getByText('移到第 2 位')).toBeVisible();
    await singleInput.fill('1');
    await expect(single.getByText('移到第 1 位')).toBeVisible();
    await single.getByRole('button', { name: '移动', exact: true }).click();
    await expect(single).toHaveCount(0);
    await page.getByRole('searchbox', { name: '搜索节点' }).fill('');
    await expect.poll(mineInOrder).toEqual([ids[4], ids[0], ids[2], ids[1], ids[3]]);
    expect(await positionsMatchList()).toBe(true);
    await expect(handle(4)).toHaveText('1');
    await expect(handle(3)).toHaveText(String(base + 4));
    await page.screenshot({ path: testInfo.outputPath('node-move-desktop.png'), fullPage: true });
  } finally {
    for (const id of ids) await rpc(page, 'DeleteNode', { id });
  }
});

test('在线更新展示实际平台能力并禁止不支持的更新', async ({ page }, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  await page.goto('/admin/updates');
  await expect(page.getByRole('heading', { name: '在线更新', exact: true })).toBeVisible();
  await expect(page.getByText(/不支持在线更新：/).first()).toBeVisible();
  await expect(page.getByRole('button', { name: '更新 Hub', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: '更新选中节点（0）', exact: true })).toBeDisabled();
  await page.setViewportSize({ width: 1440, height: 960 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440);
  await page.screenshot({ path: testInfo.outputPath('updates-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
  await page.screenshot({ path: testInfo.outputPath('updates-mobile.png'), fullPage: true });
});
