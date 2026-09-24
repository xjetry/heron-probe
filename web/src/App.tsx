import { createBrowserRouter } from "react-router";
import { Layout } from "./components/Layout";
import { Login } from "./pages/Login";
import { Overview } from "./pages/Overview";
import { NodeDetail } from "./pages/NodeDetail";
import { Nodes } from "./pages/Nodes";
import { RegisterWindow } from "./pages/RegisterWindow";
import { ProbeTasks } from "./pages/ProbeTasks";

// basename 与 hub 的挂载路径一致。路由不做鉴权判断：谁都能打开任何页面，
// 页面里的第一次请求得到 Unauthenticated 就会被数据层送去登录。
export const router = createBrowserRouter(
  [
    { path: "/login", Component: Login },
    {
      path: "/",
      Component: Layout,
      children: [
        { index: true, Component: Overview },
        { path: "nodes/:id", Component: NodeDetail },
        { path: "nodes", Component: Nodes },
        { path: "probes", Component: ProbeTasks },
        { path: "register", Component: RegisterWindow },
      ],
    },
  ],
  { basename: "/admin" },
);
