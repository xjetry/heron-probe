import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";

import { asyncUtilTimeout } from "./src/test/async-timeout.ts";

// 两个入口各自打包，产物直接落在 internal/hub/web 的 embed 目录，不再拷贝一次：
// 默认模式是管理面板（web/index.html，base /admin/，落到 dist）；--mode public 是公开页
// （根为 src/public，base /，落到 dist-public）。base 必须与 hub 的挂载路径一致，产物里的资源引用才能命中。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum 生成 TS enum，那条限制会拒绝生成代码。
const embedDir = (name: string) => fileURLToPath(new URL(`../internal/hub/web/${name}`, import.meta.url));

// Vite 清空输出目录，也由构建自身恢复占位文件，保证任意构建入口都维持 embed 目录含文件（go:embed 的模式要能匹配）。
function keepEmbedDirectory(outDir: string): Plugin {
  return { name: "keep-embed-directory", apply: "build", closeBundle() { writeFileSync(`${outDir}/.gitkeep`, ""); } };
}

export default defineConfig(({ mode }) => {
  const isPublic = mode === "public";
  const outDir = embedDir(isPublic ? "dist-public" : "dist");
  return {
    root: isPublic ? fileURLToPath(new URL("./src/public", import.meta.url)) : undefined,
    base: isPublic ? "/" : "/admin/",
    plugins: [react(), keepEmbedDirectory(outDir)],
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
