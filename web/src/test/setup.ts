import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// 本工程 globals: false，RTL 找不到全局 afterEach 就不会自动注册清理；这里显式注册。
afterEach(cleanup);
