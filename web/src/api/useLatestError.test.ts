import { act, renderHook } from "@testing-library/react";
import { expect, it } from "vitest";
import { useLatestError } from "./useLatestError";

it("只展示最新操作的错误，较早操作迟到不覆盖", () => {
  const { result } = renderHook(useLatestError);
  let first: number;
  let second: number;
  act(() => { first = result.current.onMutate(); });
  act(() => { result.current.onError("first error", {}, first); });
  expect(result.current.error).toBe("first error");
  act(() => { second = result.current.onMutate(); });
  expect(result.current.error).toBeNull();
  act(() => { result.current.onError("late first error", {}, first); });
  expect(result.current.error).toBeNull();
  act(() => { result.current.onError("second error", {}, second); });
  expect(result.current.error).toBe("second error");
});
