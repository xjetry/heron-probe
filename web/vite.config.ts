import react from "@vitejs/plugin-react";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";

import { asyncUtilTimeout } from "./src/test/async-timeout.ts";

// 两个入口各自打包，产物直接落在 internal/hub/web 的 embed 目录，不再拷贝一次：
// 默认模式是管理面板（web/index.html，base /admin/，落到 dist）；--mode public 是公开页
// （根为 src/public，base /，落到 dist-public）。base 必须与 hub 的挂载路径一致，产物里的资源引用才能命中。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum 生成 TS enum，那条限制会拒绝生成代码。
const embedDir = (name: string) => fileURLToPath(new URL(`../internal/hub/web/${name}`, import.meta.url));

// Vite 清空输出目录，也由构建自身恢复占位文件，保证任意构建入口都维持 embed 目录含文件（go:embed 的模式要能匹配）。
// 不落盘的构建（build.write 为 false，importScan.test 读模块图用）既不清空也不产出，不碰 embed 目录。
function keepEmbedDirectory(outDir: string): Plugin {
  let write = true;
  return {
    name: "keep-embed-directory",
    apply: "build",
    configResolved(config) { write = config.build.write; },
    closeBundle() { if (write) writeFileSync(`${outDir}/.gitkeep`, ""); },
  };
}

// 内置配色只写在 styles.css 的 --accent: light-dark(浅色, 深色) 里。外观页的取色器在主色为空时显示浅色那一个值，
// 这里在构建与测试时读出它、经 define 编成常量（lib/palette.ts），不另存一份；写法变了读不出即构建失败。
// 不在运行时用 ?raw 读 styles.css：vitest 默认把 CSS 文件换成空内容，测试里读不到。
function builtInLightAccent(): string {
  const css = readFileSync(new URL("./src/styles.css", import.meta.url), "utf8");
  const m = /--accent:\s*light-dark\(\s*(#[0-9a-fA-F]{6})\s*,/.exec(css);
  if (!m) throw new Error("src/styles.css: --accent must be light-dark(#rrggbb, …); the appearance page reads the built-in light accent from it");
  return m[1].toLowerCase();
}

export default defineConfig(({ mode }) => {
  const isPublic = mode === "public";
  const outDir = embedDir(isPublic ? "dist-public" : "dist");
  return {
    root: isPublic ? fileURLToPath(new URL("./src/public", import.meta.url)) : undefined,
    base: isPublic ? "/" : "/admin/",
    plugins: [react(), keepEmbedDirectory(outDir)],
    define: { __BUILT_IN_ACCENT__: JSON.stringify(builtInLightAccent()) },
    build: { outDir, emptyOutDir: true },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
      globals: false,
      // 必须长于异步查找上界，否则运行器会先杀掉用例，上界到不了。
      testTimeout: asyncUtilTimeout * 2,
    },
  };
});
