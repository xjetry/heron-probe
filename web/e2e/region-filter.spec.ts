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
    const tiles = page.locator('.tile');
    await expect(tiles).toHaveCount(6);
    const regions = page.getByRole('group', { name: '地区' });
    await regions.getByRole('button', { name: /^地区/ }).click();
    await expect(regions.locator('label > span:first-of-type')).toHaveText(['香港', '日本', '美国', '未知']);
    await regions.getByRole('checkbox', { name: '香港' }).check();
    await regions.getByRole('checkbox', { name: '日本' }).check();
    await regions.getByRole('checkbox', { name: '日本' }).press('Escape');
    const tags = page.getByRole('group', { name: '标签' });
    await tags.getByRole('button', { name: /^标签/ }).click();
    await tags.getByRole('checkbox', { name: '家宽' }).check();
    await tags.getByRole('checkbox', { name: '家宽' }).press('Escape');
    await expect(tiles).toHaveCount(2);
    await expect(tiles.locator('.tile-name')).toHaveText([`香港家宽-${browserName}`, `日本家宽-${browserName}`]);
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.screenshot({ path: testInfo.outputPath('regions-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath('regions-mobile.png'), fullPage: true });
    await page.getByRole('button', { name: '移除 香港' }).click();
    await page.getByRole('button', { name: '移除 日本' }).click();
    await expect(tiles).toHaveCount(4);
    await regions.getByRole('button', { name: /^地区/ }).click();
    await regions.getByRole('checkbox', { name: '未知' }).check();
    await regions.getByRole('checkbox', { name: '未知' }).press('Escape');
    await expect(tiles).toHaveCount(1);
    await expect(tiles.locator('.tile-name')).toHaveText([`待探测-${browserName}`]);
  } finally {
    for (const id of ids) await rpc(page, 'DeleteNode', { id });
  }
});
