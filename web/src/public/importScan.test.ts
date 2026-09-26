// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync, statSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const src = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const adminGen = resolve(src, "gen/probe/v1/admin_pb.ts");
// import / export … from "…"、副作用 import "…" 与动态 import("…")；类型 import 同样计入：
// 卫生规则针对源码依赖，不只针对打包结果。
const SPECIFIER = /\b(?:import|export)\b[^"'`;]*?\bfrom\s*["']([^"']+)["']|\bimport\s*\(?\s*["']([^"']+)["']/g;

function resolveImport(dir, spec) {
  const base = resolve(dir, spec);
  for (const candidate of [base, `${base}.ts`, `${base}.tsx`, resolve(base, "index.ts"), resolve(base, "index.tsx")]) {
    if (statSync(candidate, { throwIfNoEntry: false })?.isFile()) return candidate;
  }
  throw new Error(`cannot resolve ${spec} from ${relative(src, dir)}`);
}

// reachable 从入口沿相对 import 走到底，返回触达的每个文件与引入它的文件。包名 import（react、@connectrpc/…）
// 不展开：它们不会反向引用本仓库的生成代码。
function reachable(entry) {
  const parent = new Map([[entry, null]]);
  const stack = [entry];
  while (stack.length > 0) {
    const file = stack.pop();
    if (!/\.(ts|tsx)$/.test(file)) continue;
    for (const m of readFileSync(file, "utf8").matchAll(SPECIFIER)) {
      const spec = m[1] ?? m[2];
      if (!spec.startsWith(".")) continue;
      const next = resolveImport(dirname(file), spec);
      if (!parent.has(next)) {
        parent.set(next, file);
        stack.push(next);
      }
    }
  }
  return parent;
}

function chain(parent, file) {
  const out = [];
  for (let f = file; f; f = parent.get(f)) out.unshift(relative(src, f));
  return out.join(" → ");
}

it("公开入口不触达管理服务的生成代码", () => {
  const parent = reachable(resolve(src, "public/main.tsx"));
  // 冒烟：确实走进了公开服务的生成代码与共用组件，空集不能冒充通过。
  for (const f of ["gen/probe/v1/public_pb.ts", "gen/probe/v1/query_pb.ts", "components/History.tsx", "components/Chart.tsx", "styles.css"]) {
    expect(parent.has(resolve(src, f)), f).toBe(true);
  }
  expect(parent.has(adminGen), parent.has(adminGen) ? chain(parent, adminGen) : "").toBe(false);
});

it("同一套扫描从面板入口看得到 admin_pb", () => {
  expect(reachable(resolve(src, "main.tsx")).has(adminGen)).toBe(true);
});
