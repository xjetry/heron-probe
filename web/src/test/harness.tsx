import type { DescService } from "@bufbuild/protobuf";
import { createRouterTransport, type ServiceImpl } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export type AdminImpl = Partial<ServiceImpl<typeof AdminService>>;

// 页面测试使用内存服务和路由并关闭重试，不安装全局认证跳转；生产 client 的跳转由入口测试验证。
export function renderWithService<S extends DescService>(service: S, impl: Partial<ServiceImpl<S>>, routes: RouteObject[], initialPath: string) {
  const transport = createRouterTransport(({ service: register }) => {
    register(service, impl);
  });
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return { router, queryClient };
}

export function renderWithAdmin(impl: AdminImpl, routes: RouteObject[], initialPath: string) {
  return renderWithService(AdminService, impl, routes, initialPath);
}
