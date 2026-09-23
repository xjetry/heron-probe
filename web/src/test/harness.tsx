import { createRouterTransport, type ServiceImpl } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export type AdminImpl = Partial<ServiceImpl<typeof AdminService>>;

// 与生产入口同样的 Provider 栈，只是传输换成内存里的服务实现、路由换成内存历史。
export function renderWithAdmin(impl: AdminImpl, routes: RouteObject[], initialPath: string, extra?: ReactNode) {
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, impl);
  });
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
        {extra}
      </QueryClientProvider>
    </TransportProvider>,
  );
  return { router, queryClient };
}
