import { afterEach, expect, it, vi } from "vitest";
import { PUBLIC_VIEW_KEY, PUBLIC_WALL_GROUP_KEY, readPublicView, readWallGroupBy, writePublicView, writeWallGroupBy } from "./prefs";

afterEach(() => { vi.restoreAllMocks(); localStorage.clear(); });

it("没选过时是卡片；写入后按记住的视图读出", () => {
  expect(readPublicView()).toBe("cards");
  writePublicView("wall");
  expect(localStorage.getItem(PUBLIC_VIEW_KEY)).toBe("wall");
  expect(readPublicView()).toBe("wall");
  writePublicView("cards");
  expect(localStorage.getItem(PUBLIC_VIEW_KEY)).toBe("cards");
  expect(readPublicView()).toBe("cards");
});

it.each(["table", "", "WALL"])("记住的值 %j 不认识时按卡片", (value) => {
  localStorage.setItem(PUBLIC_VIEW_KEY, value);
  expect(readPublicView()).toBe("cards");
});

it("存储被禁用时读得到卡片、写入不抛", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
  expect(readPublicView()).toBe("cards");
  expect(() => writePublicView("wall")).not.toThrow();
});

it("状态墙分组没选过时是地区；写入后按记住的分组读出；不认识的值按地区", () => {
  expect(readWallGroupBy()).toBe("region");
  writeWallGroupBy("tag");
  expect(localStorage.getItem(PUBLIC_WALL_GROUP_KEY)).toBe("tag");
  expect(readWallGroupBy()).toBe("tag");
  localStorage.setItem(PUBLIC_WALL_GROUP_KEY, "country");
  expect(readWallGroupBy()).toBe("region");
});

it("存储被禁用时分组读得到地区、写入不抛", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
  expect(readWallGroupBy()).toBe("region");
  expect(() => writeWallGroupBy("tag")).not.toThrow();
});

