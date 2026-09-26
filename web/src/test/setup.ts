import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

import { asyncUtilTimeout } from "./async-timeout";

// 本工程 globals: false，RTL 找不到全局 afterEach 就不会自动注册清理；这里显式注册。
afterEach(cleanup);

// 上界只为挂死时能结束，不参与被测性质。显式传入 timeout 的查找不受这里影响。
configure({ asyncUtilTimeout });
