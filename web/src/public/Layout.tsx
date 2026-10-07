import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";
import { useEffect, useState } from "react";
import { Link, Outlet } from "react-router";
import { errorBanner } from "../api/queryGate";
import { PublicService, PublicSiteSchema } from "../gen/heron/v1/public_pb";
import { applySite, DEFAULT_TITLE } from "./site";
import { HeronMark } from "../components/HeronMark";
import { PUBLIC_SCHEME_KEY, readSchemeChoice, writeSchemeChoice } from "../lib/scheme";
import { POLL_MS } from "../lib/poll";
import { ThemeToggle } from "../components/ThemeToggle";

// 外观只在页面加载时取，之后不再重取：已打开的页面刷新后才看到改动，刷新时浏览器还可能再用最多 5 分钟的缓存
// （hub 对 GetSite 下发 max-age=300）。react-query 的窗口聚焦、断网重连与重新挂载三种自动重取都只针对已过期的查询，
// staleTime: Infinity 让这条永不过期，因此三种都不重取它；main.tsx 里关掉聚焦重取是全站的取舍，不是这条的前提。
//
// 没取到站点设置（加载中、失败或被限流）与设置全部为空是同一个状态：文档标题与页头都按内置外观，从同一份值得出。
export function PublicLayout() {
  const site = useQuery(PublicService.method.getSite, {}, { staleTime: Infinity });
  // 访客选择由 forcedTheme 优先于站点设置合成；存储不可用时仍由组件状态驱动当前页面。
  const [choice, setChoice] = useState(() => readSchemeChoice(PUBLIC_SCHEME_KEY));
  useEffect(() => applySite(site.data ?? create(PublicSiteSchema), choice), [site.data, choice]);
  return (
    <div className="layout public-layout">
      <header className="public-header">
        <Link to="/" className="brand">
          {site.data?.logo ? <img src={site.data.logo} alt="" className="logo" /> : <HeronMark />}
          {site.data?.title || DEFAULT_TITLE}
        </Link>
        {/* Overview 按 POLL_MS 自动刷新快照，顶栏仅说明刷新周期。 */}
        <span className="live muted">实时 · 每 {POLL_MS / 1000} 秒</span>
        <ThemeToggle choice={choice} onChange={(next) => { writeSchemeChoice(PUBLIC_SCHEME_KEY, next); setChoice(next); }} />
        {/* 面板是另一个前端入口，用普通链接整页跳转；只接受 hub 给出的面板路径。 */}
        {site.data?.adminPath && <a href={site.data.adminPath} className="admin">登录</a>}
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
