import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:https';
import { request } from 'node:http';
import { once } from 'node:events';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const dir = mkdtempSync(join(tmpdir(), 'heron-browser-'));
const db = join(dir, 'hub.db');
const binary = new URL('../../bin/heron-hub', import.meta.url).pathname;
const password = 'local-browser-test-password';
const key = join(dir, 'key.pem'), cert = join(dir, 'cert.pem');
const tls = spawnSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost'], { encoding: 'utf8' });
if (tls.status !== 0) throw new Error(tls.stderr);
const setup = spawnSync(binary, ['passwd', '--db', db], { input: password + '\n', encoding: 'utf8' });
if (setup.status !== 0) throw new Error(setup.stderr);
let closing = false, restarting = false;
function startHub() {
  const child = spawn(binary, ['serve', '--db', db, '--listen', '127.0.0.1:18987', '--trusted-proxies', '127.0.0.1/32'], { stdio: 'inherit' });
  child.on('exit', code => {
    if (restarting) return;
    rmSync(dir, { recursive: true, force: true });
    process.exit(closing ? 0 : code || 1);
  });
  return child;
}
let hub = startHub();
// 实际终止 TLS 并转发协议，让浏览器与 hub 按部署时同一条可信代理链确定来源。
const proxy = createServer({ key: readFileSync(key), cert: readFileSync(cert) }, async (req, res) => {
  // 此控制口只在回环测试代理内存在，重启沿用数据库，不能通过重新建库伪造持久化验收。
  if (req.method === 'POST' && req.url === '/__e2e/restart') {
    restarting = true;
    const exited = once(hub, 'exit');
    hub.kill('SIGTERM');
    await exited;
    hub = startHub();
    restarting = false;
    res.end('restarted');
    return;
  }
  const upstream = request({ hostname: '127.0.0.1', port: 18987, method: req.method, path: req.url, headers: { ...req.headers, 'x-forwarded-proto': 'https' } }, response => {
    res.writeHead(response.statusCode, response.headers);
    response.pipe(res);
  });
  upstream.on('error', () => { res.writeHead(502); res.end(); });
  req.pipe(upstream);
});
proxy.listen(18988, '127.0.0.1');
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => { closing = true; proxy.close(); hub.kill('SIGTERM'); });
