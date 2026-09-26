import { useQuery } from "@connectrpc/connect-query";
import { useEffect } from "react";
import { Link, Outlet } from "react-router";
import { errorBanner } from "../api/queryGate";
import { PublicService } from "../gen/probe/v1/public_pb";
import { applySite, DEFAULT_TITLE } from "./site";

// 外观只在页面加载时取，之后不再重取：已打开的页面刷新后才看到改动，刷新时浏览器还可能再用最多 5 分钟的缓存
// （hub 对 GetSite 下发 max-age=300）。"不再重取"由 staleTime: Infinity 承载：窗口聚焦重取已在 QueryClient 关掉，
// 断网重连与重新挂载只重取已过期的查询，永不过期的这条不在其列。
export function PublicLayout() {
  const site = useQuery(PublicService.method.getSite, {}, { staleTime: Infinity });
  useEffect(() => (site.data ? applySite(site.data) : undefined), [site.data]);
  return (
    <div className="layout">
      <header className="nav">
        <Link to="/" className="brand">
          {site.data?.logo && <img src={site.data.logo} alt="" className="logo" />}
          {site.data?.title || DEFAULT_TITLE}
        </Link>
      </header>
      <main className="main">
        {errorBanner(site.error)}
        <Outlet />
      </main>
    </div>
  );
}

export function NotFound() {
  return <p className="muted">页面不存在。<Link to="/">返回总览</Link></p>;
}
