import { GROUP_BYS, type GroupBy } from "./filters";

export type View = "wall" | "cards";
export const PUBLIC_VIEW_KEY = "heron-public-view";
export const PUBLIC_WALL_GROUP_KEY = "heron-public-wall-group";
const VIEWS: readonly View[] = ["wall", "cards"];

// 访客的视图与状态墙分组按浏览器记住。没选过、存储不可用或记住的值不认识时用默认值：视图默认卡片（卡片视图只放在线
// 与维护中节点，离线与从未上报列在网格下方），分组默认地区。写不进去只影响下次打开，不改变当前页面已切换的选择。
export const readPublicView = (): View => readChoice(PUBLIC_VIEW_KEY, VIEWS, "cards");
export const writePublicView = (view: View): void => writeChoice(PUBLIC_VIEW_KEY, view);
export const readWallGroupBy = (): GroupBy => readChoice(PUBLIC_WALL_GROUP_KEY, GROUP_BYS.map((option) => option.value), "region");
export const writeWallGroupBy = (by: GroupBy): void => writeChoice(PUBLIC_WALL_GROUP_KEY, by);

function readChoice<T extends string>(key: string, choices: readonly T[], fallback: T): T {
  try {
    const value = localStorage.getItem(key);
    return choices.includes(value as T) ? (value as T) : fallback;
  } catch {
    return fallback;
  }
}

function writeChoice(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // 持久化是尽力而为。
  }
}
