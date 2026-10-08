import { expect, test, type Page } from "@playwright/test";

async function rpc(page: Page, method: string, body: unknown = {}, token = "") {
  return page.evaluate(async ({ method, body, token }) => {
    const response = await fetch(`/heron.v1.AdminService/${method}`, {
      method: "POST",
      credentials: token ? "omit" : "same-origin",
      headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify(body),
    });
    return { status: response.status, body: await response.json() };
  }, { method, body, token });
}

test("限定节点凭据的创建、预览、写入、重试、回执和吊销", async ({ page, browserName }, testInfo) => {
  await page.goto("/admin/login");
  expect((await rpc(page, "Login", { password: "local-browser-test-password" })).status).toBe(200);
  const ids: string[] = [];
  const name = `scoped-${browserName}`;
  let tokenId = "";
  try {
    for (const suffix of ["allowed", "outside"]) {
      const created = await rpc(page, "CreateNode", { name: `${name}-${suffix}` });
      expect(created.status).toBe(200);
      ids.push(created.body.node.id);
    }
    await page.goto("/admin/tokens");
    await page.getByRole("button", { name: "新建 API token", exact: true }).click();
    const form = page.getByRole("form", { name: "新建 API token" });
    await form.getByLabel("名称", { exact: true }).fill(name);
    await form.getByLabel("监控配置", { exact: true }).check();
    await form.getByLabel("节点范围").selectOption("selected");
    await form.getByLabel(`${name}-allowed（#${ids[0]}）`, { exact: true }).check();
    await form.getByRole("button", { name: "创建", exact: true }).click();
    const secret = page.locator("code.secret");
    await expect(secret).toBeVisible();
    const token = (await secret.textContent())!;
    const tokens = await rpc(page, "ListApiTokens");
    const created = tokens.body.tokens.find((entry: { name: string }) => entry.name === name);
    tokenId = created.id;
    expect(created.grant.permissions).toEqual(["TOKEN_PERMISSION_CONFIGURE"]);
    expect(created.grant.allNodes ?? false).toBe(false);
    expect(created.grant.nodeIds).toEqual([ids[0]]);
    const scoped = await rpc(page, "ListNodes", {}, token);
    expect(scoped.status).toBe(200);
    expect(scoped.body.nodes.map((node: { id: string }) => node.id)).toEqual([ids[0]]);

    const change = { requestId: `browser-${browserName}`, updateMask: "note", updateNode: { id: ids[0], note: "browser-verified" } };
    const preview = await rpc(page, "ExecuteChange", { ...change, preview: true }, token);
    expect(preview.status).toBe(200);
    expect(preview.body.expectedVersion).toBeTruthy();
    expect(preview.body.operation.committedAt ?? "0").toBe("0");
    expect(preview.body.result).toBeUndefined();
    const before = await rpc(page, "ListNodes", {}, token);
    expect(before.body.nodes[0].note ?? "").toBe("");
    const denied = await rpc(page, "ExecuteChange", { ...change, preview: true, updateNode: { id: ids[1], note: "forbidden" } }, token);
    expect(denied.body.code).toBe("permission_denied");
    const execution = { ...change, expectedVersion: preview.body.expectedVersion };
    const committed = await rpc(page, "ExecuteChange", execution, token);
    expect(committed.status).toBe(200);
    expect(committed.body.operation.id).toBeTruthy();
    expect(Number(committed.body.operation.committedAt)).toBeGreaterThan(0);
    const after = await rpc(page, "ListNodes", {}, token);
    expect(after.body.nodes[0].note).toBe("browser-verified");
    expect(after.body.nodes[0].name).toBe(`${name}-allowed`);
    const replay = await rpc(page, "ExecuteChange", execution, token);
    expect(replay.status).toBe(200);
    expect(replay.body.replayed).toBe(true);
    expect(replay.body.operation.id).toBe(committed.body.operation.id);
    expect(replay.body.result).toBeUndefined();

    await page.getByRole("dialog").getByRole("button", { name: "关闭抽屉", exact: true }).click();
    await page.getByRole("button", { name: `更多操作 ${name}（#${tokenId}）`, exact: true }).click();
    await page.getByRole("menuitem", { name: `查看操作记录 ${name}（#${tokenId}）`, exact: true }).click();
    const operations = page.getByRole("region", { name: "操作记录" });
    await expect(operations.locator("details")).toHaveCount(1);
    await operations.locator("summary").click();
    await expect(operations.getByText(change.requestId, { exact: true })).toBeVisible();
    await expect(operations.getByText(committed.body.operation.id, { exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("token-audit-desktop.png"), fullPage: true });
    await page.setViewportSize({ width: 375, height: 812 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(375);
    await page.screenshot({ path: testInfo.outputPath("token-audit-mobile.png"), fullPage: true });
    await page.getByRole("button", { name: `更多操作 ${name}（#${tokenId}）`, exact: true }).click();
    await page.getByRole("menuitem", { name: `吊销 ${name}（#${tokenId}）`, exact: true }).click();
    await page.getByRole("menuitem", { name: `确认吊销 ${name}（#${tokenId}）`, exact: true }).click();
    await page.getByRole("button", { name: `更多操作 ${name}（#${tokenId}）`, exact: true }).waitFor({ state: "hidden" });
    await expect(secret).toHaveCount(0);
    const revoked = await rpc(page, "ListNodes", {}, token);
    expect(revoked.body.code).toBe("unauthenticated");
  } finally {
    if (tokenId) await rpc(page, "DeleteApiToken", { id: tokenId });
    for (const id of ids) await rpc(page, "DeleteNode", { id });
  }
});
