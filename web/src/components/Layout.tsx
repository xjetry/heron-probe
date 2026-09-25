import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";

export function Layout() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const logout = useMutation(AdminService.method.logout, {
    onSuccess: () => { queryClient.clear(); void navigate("/login", { replace: true }); },
  });
  const { reset } = logout;
  useEffect(() => { reset(); }, [location, reset]);
  return (
    <div className="layout">
      <nav className="nav" aria-label="主导航">
        <span className="brand">probe</span>
        <NavLink to="/" end>总览</NavLink>
        <NavLink to="/nodes">节点</NavLink>
        <NavLink to="/probes">探测任务</NavLink>
        <NavLink to="/alerts">告警规则</NavLink>
        <NavLink to="/events">告警事件</NavLink>
        <NavLink to="/channels">通知渠道</NavLink>
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
