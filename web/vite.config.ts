import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// base 与 hub 的挂载路径一致：产物里的资源引用都是 /admin/assets/…，
// 由 internal/hub/web 服务。outDir 直接落在 embed 目录，不再拷贝一次。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum
// 生成 TS enum，那条限制会拒绝生成代码。
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: { outDir: "../internal/hub/web/dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    globals: false,
  },
});
