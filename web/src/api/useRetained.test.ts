import { renderHook } from "@testing-library/react";
import { expect, it } from "vitest";
import { useRetained } from "./useRetained";

type Q = { data: number | undefined; error: unknown };
type QWithIdentity = Q & { identity: string };

it("请求失败时沿用上一份成功数据，data 恢复后不再沿用", () => {
  const initialProps: Q = { data: 1, error: null };
  const { result, rerender } = renderHook((q: Q) => useRetained(q), { initialProps });
  expect(result.current).toEqual({ data: 1, error: null, stale: false });
  rerender({ data: undefined, error: "boom" });
  expect(result.current).toEqual({ data: 1, error: "boom", stale: true });
  rerender({ data: 2, error: null });
  expect(result.current).toEqual({ data: 2, error: null, stale: false });
});

it("identity 变化的这一帧起不沿用上一个对象的数据，直到新对象自己取到数据", () => {
  const initialProps: QWithIdentity = { data: 1, error: null, identity: "a" };
  const { result, rerender } = renderHook(({ data, error, identity }: QWithIdentity) => useRetained({ data, error }, identity), { initialProps });
  expect(result.current).toEqual({ data: 1, error: null, stale: false });
  // 换对象：新对象还没有自己的数据，不该沿用 "a" 的 1。
  rerender({ data: undefined, error: null, identity: "b" });
  expect(result.current).toEqual({ data: undefined, error: null, stale: false });
  // 同一个新对象继续挂起或失败：还是不沿用 "a" 的值。
  rerender({ data: undefined, error: "b failed", identity: "b" });
  expect(result.current).toEqual({ data: undefined, error: "b failed", stale: false });
  // "b" 自己的数据到达。
  rerender({ data: 2, error: null, identity: "b" });
  expect(result.current).toEqual({ data: 2, error: null, stale: false });
  // 现在 "b" 的刷新失败，沿用的是 "b" 的 2，不是 "a" 的 1。
  rerender({ data: undefined, error: "b refresh failed", identity: "b" });
  expect(result.current).toEqual({ data: 2, error: "b refresh failed", stale: true });
});

it("不传 identity 时行为不变：调用点本身就是身份，Nodes 页换过滤条件属于这一种", () => {
  const initialProps: Q = { data: 1, error: null };
  const { result, rerender } = renderHook((q: Q) => useRetained(q), { initialProps });
  rerender({ data: undefined, error: null });
  expect(result.current).toEqual({ data: 1, error: null, stale: true });
});
