import { test, expect, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";

// 固定测试密码只用于每次临时创建的本机数据库，服务器退出后整个目录删除。
const password = "local-browser-test-password";
function zip(files: Record<string, string>): Buffer {
  const parts: Buffer[] = [], directory: Buffer[] = [];
  let offset = 0;
  for (const [name, content] of Object.entries(files)) {
    const path = Buffer.from(name), bytes = Buffer.from(content);
    let crc = 0xffffffff;
    for (const byte of bytes) { crc ^= byte; for (let i = 0; i < 8; i++) crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1; }
    crc = (crc ^ 0xffffffff) >>> 0;
    const header = Buffer.alloc(30); header.writeUInt32LE(0x04034b50); header.writeUInt16LE(20, 4); header.writeUInt32LE(crc, 14); header.writeUInt32LE(bytes.length, 18); header.writeUInt32LE(bytes.length, 22); header.writeUInt16LE(path.length, 26);
    const central = Buffer.alloc(46); central.writeUInt32LE(0x02014b50); central.writeUInt16LE(20, 4); central.writeUInt16LE(20, 6); central.writeUInt32LE(crc, 16); central.writeUInt32LE(bytes.length, 20); central.writeUInt32LE(bytes.length, 24); central.writeUInt16LE(path.length, 28); central.writeUInt32LE(offset, 42);
    parts.push(header, path, bytes); directory.push(central, path); offset += header.length + path.length + bytes.length;
  }
  const central = Buffer.concat(directory), end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50); end.writeUInt16LE(directory.length / 2, 8); end.writeUInt16LE(directory.length / 2, 10); end.writeUInt32LE(central.length, 12); end.writeUInt32LE(offset, 16);
  return Buffer.concat([...parts, central, end]);
}

async function rpc(page: Page, method: string, body: unknown = {}) {
  return page.evaluate(async ({ method, body }) => {
    const response = await fetch('/heron.v1.AdminService/' + method, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const result = await response.json();
    if (!response.ok) throw new Error(method + ': ' + JSON.stringify(result));
    return result;
  }, { method, body });
}
async function login(page: Page) {
  await page.goto('/admin/login');
  await rpc(page, 'Login', { password });
}

test('安装、预览、启用、旧资源及直接文档都保持权限隔离', async ({ page, context, browserName }, testInfo) => {
  await login(page);
  await rpc(page, 'UpdateSettings', { settings: { publicEnabled: true } });
  const id = 'browser-' + browserName;
  const pkg = zip({
    'theme.json': JSON.stringify({ id, name: 'Browser Theme', version: '1', sdk: 1 }),
    'index.html': '<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="./style.css"><div id="result">loading</div><button id="route">节点</button><script type="module" src="./app.js"></script>',
    'style.css': 'body{margin:0;background:rgb(12, 34, 56);color:white}#result{overflow-wrap:anywhere}',
    'dynamic.js': 'export default "dynamic-ok";',
    'view.svg': '<svg xmlns="http://www.w3.org/2000/svg"><script>try{document.cookie;document.documentElement.setAttribute("unsafe","yes")}catch(e){document.documentElement.setAttribute("isolated","yes")}</script></svg>',
    'app.js': `import {getSite,getSnapshot,navigate,onRoute,call} from '/_heron/theme-sdk.js';
const result={};
for(const [key,action] of Object.entries({parent:()=>parent.document.body, cookie:()=>document.cookie,storage:()=>localStorage.getItem('x')})) {try{action();result[key]=false}catch(e){result[key]=true}}
try{await fetch('/heron.v1.AdminService/CreateNode',{method:'POST',credentials:'include',headers:{'Content-Type':'application/json'},body:'{"name":"stolen"}'});result.fetch=false}catch(e){result.fetch=true}
try{await navigator.serviceWorker.register('./worker.js');result.worker=false}catch(e){result.worker=true}
try{await navigator.credentials.get({publicKey:{challenge:new Uint8Array(32),timeout:50}});result.passkey=false}catch(e){result.passkey=true}
result.dynamic=(await import('./dynamic.js')).default;
result.css=getComputedStyle(document.body).backgroundColor;
if(parent!==window){const site=await getSite();result.site=typeof site==='object';result.adminPath=site.adminPath??null;result.snapshot=!!(await getSnapshot());await onRoute(path=>{document.body.dataset.route=path});document.querySelector('#route').onclick=()=>navigate('/nodes/1')}
if(parent!==window){try{await call('QueryMetrics',{nodeId:'999',from:'1',to:'2'})}catch(error){result.errorCode=error.code}}
document.querySelector('#result').textContent=JSON.stringify(result);`,
  });
  await page.goto('/admin/themes');
  await page.getByLabel('主题包（zip，至多 8 MiB）').setInputFiles({ name: 'theme.zip', mimeType: 'application/zip', buffer: pkg });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByText(/已安装 Browser Theme/)).toBeVisible();
  const downloadEvent = page.waitForEvent('download');
  await page.getByRole('button', { name: new RegExp('^下载原包 Browser Theme（' + id + '）') }).click();
  const downloaded = await downloadEvent;
  expect(await readFile((await downloaded.path())!)).toEqual(pkg);
  await page.screenshot({ path: testInfo.outputPath('themes-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 760 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
  await page.screenshot({ path: testInfo.outputPath('themes-mobile.png'), fullPage: true });
  await page.setViewportSize({ width: 1280, height: 720 });
  const { themes } = await rpc(page, 'ListThemes');
  const installed = themes.find((t: { id: string }) => t.id === id);
  expect(installed.enabled ?? false).toBe(false);
  const file = `/_heron/themes/${id}/${installed.digest}/index.html`;
  expect((await context.request.get(file)).status()).toBe(404);
  const preview = await rpc(page, 'PreviewTheme', { id, digest: installed.digest });
  const sandbox = await context.newPage();
  await sandbox.goto(preview.url);
  const frame = sandbox.frameLocator('iframe');
  await expect(frame.locator('#result')).toContainText('dynamic-ok');
  const result = JSON.parse(await frame.locator('#result').innerText());
  expect(result).toMatchObject({ parent: true, cookie: true, storage: true, fetch: true, worker: true, passkey: true, dynamic: 'dynamic-ok', css: 'rgb(12, 34, 56)', site: true, snapshot: true, adminPath: null, errorCode: 'not_found' });
  await frame.locator('#route').click();
  await expect(frame.locator('body')).toHaveAttribute('data-route', '/nodes/1');
  expect(new URL(sandbox.url()).pathname).toBe(preview.url);
  await rpc(page, 'EnableTheme', { id, digest: installed.digest });
  await sandbox.goto('/nodes/2');
  await expect(sandbox.frameLocator('iframe').locator('body')).toHaveAttribute('data-route', '/nodes/2');
  await sandbox.frameLocator('iframe').locator('#route').click();
  await expect(sandbox).toHaveURL(/\/nodes\/1$/);
  await sandbox.goBack();
  await expect(sandbox.frameLocator('iframe').locator('body')).toHaveAttribute('data-route', '/nodes/2');
  await sandbox.setViewportSize({ width: 375, height: 760 });
  expect(await sandbox.locator('iframe').evaluate(el => el.getBoundingClientRect().width)).toBe(375);
  await sandbox.goto(file);
  await expect(sandbox.locator('#result')).toContainText('dynamic-ok');
  expect(JSON.parse(await sandbox.locator('#result').innerText())).toMatchObject({ cookie: true, storage: true, fetch: true, worker: true, passkey: true });
  await sandbox.goto(file.replace('index.html', 'view.svg'));
  await expect(sandbox.locator('svg')).toHaveAttribute('isolated', 'yes');
  const nodes = await rpc(page, 'ListNodes');
  expect(nodes.nodes ?? []).toEqual([]);
  await rpc(page, 'EnableTheme', {});
  expect((await context.request.get(file)).status()).toBe(200);
  await rpc(page, 'UpdateSettings', { settings: { publicEnabled: false } });
  expect(await (await context.request.get(file)).text()).toContain('公开页已关闭');
  await rpc(page, 'UpdateSettings', { settings: { publicEnabled: true } });
  await rpc(page, 'DeleteTheme', { id });
  await sandbox.close();
});

test('Passkey 在 HTTPS 注册、重启登录并迁移到新域名', async ({ page, context, browserName }) => {
  test.skip(browserName !== 'chromium', '虚拟认证器由 Chromium CDP 提供');
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  await login(page);
  await page.goto('/admin/security');
  await page.getByRole('article', { name: 'TOTP' }).getByRole('link', { name: '管理 TOTP、恢复码与 Passkey' }).click();
  await page.getByLabel('管理员密码').fill(password);
  await page.getByLabel('认证器名称').fill('Browser key');
  await page.getByRole('button', { name: '添加 Passkey', exact: true }).click();
  await expect(page.getByRole('heading', { name: '认证方式已更新' })).toBeVisible();
  await page.goto('/admin/login');
  await page.getByRole('button', { name: /Passkey/ }).click();
  await expect(page).not.toHaveURL(/\/login$/);
  const info = await rpc(page, 'GetSecurity');
  expect(info.origin).toBe('https://localhost:18988');
  expect(info.passkeys).toHaveLength(1);
  expect((await context.request.post('/__e2e/restart')).ok()).toBe(true);
  await expect.poll(async () => {
    try { return (await rpc(page, 'GetSecurity')).origin; } catch { return ''; }
  }).toBe('https://localhost:18988');
  await rpc(page, 'Logout');
  await page.goto('/admin/login');
  await page.getByRole('button', { name: /Passkey/ }).click();
  await expect(page).not.toHaveURL(/\/login$/);
  await page.goto('https://other.localhost:18988/admin/login');
  await rpc(page, 'Login', { password });
  await page.goto('https://other.localhost:18988/admin/security/credentials');
  await expect(page.getByText('当前访问域名与已绑定域名不同')).toBeVisible();
  await page.getByLabel('管理员密码').fill(password);
  await page.getByLabel('认证器名称').fill('Replacement key');
  await page.getByRole('button', { name: '重新绑定并添加 Passkey', exact: true }).click();
  await expect(page.getByRole('heading', { name: '认证方式已更新' })).toBeVisible();
  await page.goto('https://other.localhost:18988/admin/login');
  await page.getByRole('button', { name: /Passkey/ }).click();
  await expect(page).not.toHaveURL(/\/login$/);
  const rebound = await rpc(page, 'GetSecurity');
  expect(rebound.origin).toBe('https://other.localhost:18988');
  expect(rebound.passkeys).toHaveLength(1);
  expect(rebound.passkeys[0].name).toBe('Replacement key');
});
