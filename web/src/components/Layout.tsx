import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { NavLink, Outlet, useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";

export function Layout() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const logout = useMutation(AdminService.method.logout, {
    onSuccess: () => { queryClient.clear(); void navigate("/login", { replace: true }); },
  });
  return (
    <div className="layout">
      <nav className="nav" aria-label="主导航">
        <span className="brand">probe</span>
        <NavLink to="/" end>总览</NavLink>
        <NavLink to="/nodes">节点</NavLink>
        <NavLink to="/register">注册窗口</NavLink>
        <button type="button" className="link" onClick={() => logout.mutate({})} disabled={logout.isPending}>
          登出
        </button>
      </nav>
      <main className="main">
        {logout.error && <p role="alert" className="error">{errorText(logout.error)}</p>}
        <Outlet />
      </main>
    </div>
  );
}
