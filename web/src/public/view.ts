export type View = "wall" | "cards";
export const PUBLIC_VIEW_KEY = "heron-public-view";
const VIEWS: readonly View[] = ["wall", "cards"];

// 访客上次选的视图按浏览器记住。没选过、存储不可用或记住的值不认识时用卡片：卡片视图只放在线与维护中节点，
// 离线与从未上报折在网格下方。
export function readPublicView(): View {
  try {
    const value = localStorage.getItem(PUBLIC_VIEW_KEY);
    return VIEWS.includes(value as View) ? (value as View) : "cards";
  } catch {
    return "cards";
  }
}

export function writePublicView(view: View): void {
  try {
    localStorage.setItem(PUBLIC_VIEW_KEY, view);
  } catch {
    // 持久化是尽力而为：存不进去只影响下次打开的视图，不改变当前页面已切换的视图。
  }
}
