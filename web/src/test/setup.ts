import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Vitest 不暴露全局钩子，显式清理才能隔离各用例挂载的 React 树。
afterEach(cleanup);
