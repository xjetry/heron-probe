import type { TagMatch } from "../lib/tags";
import { GROUP_BYS, type GroupBy } from "./filters";

export type View = "wall" | "cards" | "list";
export const PUBLIC_VIEW_KEY = "heron-public-view";
export const PUBLIC_WALL_GROUP_KEY = "heron-public-wall-group";
const VIEWS: readonly View[] = ["wall", "cards", "list"];

// 地区与标签筛选各自的选择方式（public/Facet.tsx）。
export type Facet = "region" | "tag";
export type FacetMode = "single" | "multi";
export const PUBLIC_FACET_MODE_KEYS: Readonly<Record<Facet, string>> = { region: "heron-public-region-mode", tag: "heron-public-tag-mode" };
const FACET_MODES: readonly FacetMode[] = ["single", "multi"];
export const PUBLIC_TAG_MATCH_KEY = "heron-public-tag-match";
const TAG_MATCHES: readonly TagMatch[] = ["all", "any"];

// 访客的视图、状态墙分组、两个筛选的选择方式与标签多选的匹配方式按浏览器记住。没选过、存储不可用或记住的值不认识时
// 用默认值：视图默认卡片（卡片视图只放在线与维护中节点，离线与从未上报列在网格下方），分组默认地区，选择方式默认单选，
// 标签匹配默认同时满足（与管理端一致）。写不进去只影响下次打开，不改变当前页面已切换的选择。
export const readPublicView = (): View => readChoice(PUBLIC_VIEW_KEY, VIEWS, "cards");
export const writePublicView = (view: View): void => writeChoice(PUBLIC_VIEW_KEY, view);
export const readWallGroupBy = (): GroupBy => readChoice(PUBLIC_WALL_GROUP_KEY, GROUP_BYS.map((option) => option.value), "region");
export const writeWallGroupBy = (by: GroupBy): void => writeChoice(PUBLIC_WALL_GROUP_KEY, by);
export const readFacetMode = (facet: Facet): FacetMode => readChoice(PUBLIC_FACET_MODE_KEYS[facet], FACET_MODES, "single");
export const writeFacetMode = (facet: Facet, mode: FacetMode): void => writeChoice(PUBLIC_FACET_MODE_KEYS[facet], mode);
export const readTagMatch = (): TagMatch => readChoice(PUBLIC_TAG_MATCH_KEY, TAG_MATCHES, "all");
export const writeTagMatch = (match: TagMatch): void => writeChoice(PUBLIC_TAG_MATCH_KEY, match);

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
