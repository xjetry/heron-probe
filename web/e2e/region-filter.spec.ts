import { expect, test, type Page } from "@playwright/test";

async function rpc(page: Page, method: string, body: unknown = {}) {
  return page.evaluate(async ({ method, body }) => {
    const response = await fetch('/heron.v1.AdminService/' + method, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error(method + ': ' + await response.text());
    return response.json();
  }, { method, body });
}

test('公开页地区多选与家宽标签交集，未知和手机布局', async ({ page, browserName }, testInfo) => {
  const ids: string[] = [];
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password: 'local-browser-test-password' });
  await rpc(page, 'UpdateSettings', { settings: { publicEnabled: true } });
  const fixtures = [
    ['香港家宽', 'HK', '家宽'], ['日本家宽', 'JP', '家宽'], ['美国家宽', 'US', '家宽'],
    ['香港机房', 'HK', '机房'], ['日本机房', 'JP', '机房'], ['待探测', '', '家宽'],
  ];
  try {
    for (const [label, countryPin, tag] of fixtures) {
      const name = `${label}-${browserName}`;
      const id = (await rpc(page, 'CreateNode', { name })).node.id;
      ids.push(id);
      await rpc(page, 'UpdateNode', { id, name, public: true, countryPin, tags: [tag], trafficResetDay: 1, offlineGraceS: 0 });
    }
    await page.goto('/');
    const regions = page.getByRole('group', { name: '按地区筛选' });
    const cards = page.getByRole('article');
    await expect(cards).toHaveCount(6);
    await expect(regions.getByRole('button')).toHaveText(['全部', 'HK', 'JP', 'US', '未知']);
    await expect(regions.getByRole('button', { name: '全部', exact: true })).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByText('地区', { exact: true })).toHaveCount(0);
    await expect(page.getByText('可多选，未选则显示全部地区', { exact: true })).toHaveCount(0);
    await page.getByRole('group', { name: '按标签筛选' }).getByRole('button', { name: '家宽', exact: true }).click();
    await regions.getByRole('button', { name: 'HK', exact: true }).click();
    await regions.getByRole('button', { name: 'JP', exact: true }).click({ modifiers: ['Shift'] });
    await expect(cards).toHaveCount(2);
    await expect(cards.nth(0)).toHaveAttribute('aria-label', `香港家宽-${browserName}`);
    await expect(cards.nth(1)).toHaveAttribute('aria-label', `日本家宽-${browserName}`);
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.screenshot({ path: testInfo.outputPath('regions-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('regions-mobile.png'), fullPage: true });
    await regions.getByRole('button', { name: '全部', exact: true }).click();
    await expect(cards).toHaveCount(4);
    await regions.getByRole('button', { name: '未知', exact: true }).click();
    await expect(cards).toHaveCount(1);
    await expect(cards).toHaveAttribute('aria-label', `待探测-${browserName}`);
  } finally {
    for (const id of ids) await rpc(page, 'DeleteNode', { id });
  }
});
