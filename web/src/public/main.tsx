import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TransportProvider } from "@connectrpc/connect-query";
import { RouterProvider } from "react-router";
import { transport } from "./transport";
import { router } from "./router";
import "../styles.css";
import "./public.css";

// 公开页没有会话，不装认证跳转。失败的查询重试两次后交给页面的错误横幅；限流（ResourceExhausted）也一样。
// 窗口聚焦重取全站关掉：快照靠轮询、历史图靠窗口右端前进刷新，聚焦时再取一遍只多耗匿名限流的令牌。
// 站点设置不重取另由它自己的 staleTime: Infinity 承载（Layout.tsx），与这个开关无关。
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 2, refetchOnWindowFocus: false } } });

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>
  </StrictMode>,
);
