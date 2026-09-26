import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";
import { defineConfig } from "vitest/config";

import { asyncUtilTimeout } from "./src/test/async-timeout.ts";

// base 与 hub 的挂载路径一致：产物里的资源引用都是 /admin/assets/…，
// 由 internal/hub/web 服务。outDir 直接落在 embed 目录，不再拷贝一次。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum
// 生成 TS enum，那条限制会拒绝生成代码。
export default defineConfig({
  base: "/admin/",
  plugins: [react(), {
    name: "keep-embed-directory",
    apply: "build",
    // Vite 清空输出目录，也由构建自身恢复占位文件，保证任意构建入口都维持 embed 目录含文件。
    closeBundle() { writeFileSync(new URL("../internal/hub/web/dist/.gitkeep", import.meta.url), ""); },
  }],
  build: { outDir: "../internal/hub/web/dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    globals: false,
    // 必须长于异步查找上界，否则运行器会先杀掉用例，上界到不了。
    testTimeout: asyncUtilTimeout * 2,
  },
});
