import { useMutation } from "@connectrpc/connect-query";
import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router";
import { AdminService } from "../gen/heron/v1/admin_pb";
import { errorText } from "../api/auth";
import { useLeaveSession } from "../api/useLeaveSession";
import { HeronMark } from "./HeronMark";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";

const navigation: { label: string; items: { to: string; label: string; icon: IconName }[] }[] = [
  { label: "监控", items: [{ to: "/", label: "总览", icon: "overview" }, { to: "/nodes", label: "节点", icon: "server" }, { to: "/probes", label: "探测任务", icon: "activity" }] },
  { label: "告警", items: [{ to: "/alerts", label: "告警规则", icon: "bell" }, { to: "/events", label: "告警事件", icon: "history" }, { to: "/channels", label: "通知渠道", icon: "send" }] },
  { label: "系统", items: [{ to: "/appearance", label: "外观", icon: "palette" }, { to: "/themes", label: "主题", icon: "layers" }, { to: "/storage", label: "存储", icon: "database" }, { to: "/security", label: "安全", icon: "shield" }, { to: "/tokens", label: "API token", icon: "key" }, { to: "/register", label: "注册窗口", icon: "plus" }] },
];

function Navigation({ onNavigate }: { onNavigate?: () => void }) {
  return <nav className="panel-nav" aria-label="主导航">{navigation.map((group) => <div className="nav-group" key={group.label}>
    <p>{group.label}</p>{group.items.map((item) => <NavLink key={item.to} to={item.to} end={item.to === "/"} onClick={onNavigate}><Icon name={item.icon} /><span>{item.label}</span></NavLink>)}
  </div>)}</nav>;
}

function savedScheme() {
  try { return localStorage.getItem("heron-admin-scheme") ?? "auto"; } catch { return "auto"; }
}

export function Layout() {
  const leaveSession = useLeaveSession();
  const location = useLocation();
  const [menuOpener, setMenuOpener] = useState<HTMLElement | null>(null);
  const [scheme, setScheme] = useState(savedScheme);
  const current = navigation.flatMap((group) => group.items).find((item) => item.to === "/" ? location.pathname === "/" : location.pathname.startsWith(item.to))?.label ?? "工作台";
  useEffect(() => {
    const previous = document.documentElement.dataset.theme;
    if (scheme === "light" || scheme === "dark") document.documentElement.dataset.theme = scheme;
    else delete document.documentElement.dataset.theme;
    return () => {
      if (previous === undefined) delete document.documentElement.dataset.theme;
      else document.documentElement.dataset.theme = previous;
    };
  }, [scheme]);
  const logout = useMutation(AdminService.method.logout, { onSuccess: leaveSession });
  const { reset } = logout;
  useEffect(() => { reset(); }, [location, reset]);
  return (
    <div className="admin-shell">
      <a href="#admin-main" className="skip-link">跳到主要内容</a>
      <aside className="admin-sidebar">
        <div className="sidebar-brand"><span className="brand"><HeronMark />Heron</span><span className="workspace-label">管理工作台</span></div>
        <Navigation />
        <div className="sidebar-footer"><span className="admin-avatar">H</span><div><strong>管理员</strong><small>基础设施监控</small></div></div>
      </aside>
      <div className="admin-workspace">
        <header className="admin-topbar">
          <button type="button" className="icon-button mobile-menu" aria-label="打开导航" aria-expanded={menuOpener !== null} onClick={(event) => setMenuOpener(event.currentTarget)}><Icon name="menu" /></button>
          <div className="admin-breadcrumb"><span>工作台</span><span aria-hidden="true">/</span><strong>{current}</strong></div>
          <div className="topbar-actions">
            <a href="/" target="_blank" rel="noreferrer" className="public-page-link"><Icon name="external" /><span>公开面板</span></a>
            <label className="scheme-control"><Icon name="sun" /><select aria-label="后台配色" value={scheme} onChange={(event) => {
              const value = event.target.value;
              setScheme(value);
              try { localStorage.setItem("heron-admin-scheme", value); } catch { /* 存储被禁用时，当前页面仍可切换。 */ }
            }}><option value="auto">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></label>
            <button type="button" className="icon-button" title="登出" aria-label="登出" onClick={() => logout.mutate({})} disabled={logout.isPending}><Icon name="logout" /></button>
          </div>
        </header>
      <main id="admin-main" className="admin-main" tabIndex={-1}>
        {logout.error && <p role="alert" className="error">{errorText(logout.error)}</p>}
        <Outlet />
      </main>
      </div>
      {menuOpener && <Modal title="导航" className="navigation-drawer" opener={menuOpener} onClose={() => setMenuOpener(null)}><Navigation onNavigate={() => setMenuOpener(null)} /></Modal>}
    </div>
  );
}
