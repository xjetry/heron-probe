import { useMutation } from "@connectrpc/connect-query";
import { NavLink, Outlet, useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export function Layout() {
  const navigate = useNavigate();
  const logout = useMutation(AdminService.method.logout, {
    onSuccess: () => void navigate("/login", { replace: true }),
  });
  return (
    <div className="layout">
      <nav className="nav" aria-label="主导航">
        <span className="brand">probe</span>
        <NavLink to="/" end>总览</NavLink>
        <button type="button" className="link" onClick={() => logout.mutate({})} disabled={logout.isPending}>
          登出
        </button>
      </nav>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
