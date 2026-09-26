import type { PublicSite } from "../gen/probe/v1/public_pb";

// 站点设置里标题为空时用的标题；面板的外观页拿它作占位提示。
export const DEFAULT_TITLE = "服务器状态";

// applySite 把站点设置应用到文档。明暗写在 html 的 data-theme 上（auto 不写，跟随系统；styles.css 的 color-scheme
// 规则按它切换），主色覆盖 --accent，自定义 CSS 放进 head 末尾的 <style>，排在全部内置样式之后。
// CSS 经 textContent 写入，不经 HTML 解析；hub 另外拒绝含 "</" 的值，那是给把它内联进 HTML 的消费者的约束。
// 返回撤销函数：设置变化时先撤掉上一份再应用新的。
export function applySite(site: PublicSite): () => void {
  const root = document.documentElement;
  if (site.theme === "light" || site.theme === "dark") root.dataset.theme = site.theme;
  else delete root.dataset.theme;
  if (site.accentColor) root.style.setProperty("--accent", site.accentColor);
  else root.style.removeProperty("--accent");
  document.title = site.title || DEFAULT_TITLE;
  const style = document.createElement("style");
  style.dataset.siteCss = "";
  style.textContent = site.customCss;
  document.head.append(style);
  return () => {
    style.remove();
    delete root.dataset.theme;
    root.style.removeProperty("--accent");
  };
}
