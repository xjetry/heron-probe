import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Secret } from "./Secret";

afterEach(() => { vi.unstubAllGlobals(); window.getSelection()?.removeAllRanges(); });

it.each(["missing", "rejected"])("剪贴板 %s 时反馈失败并选中明文", async (reason) => {
  vi.stubGlobal("navigator", reason === "missing" ? {} : { clipboard: { writeText: async () => { throw new Error("denied"); } } });
  render(<Secret label="token" value="select-this-secret" />);
  fireEvent.click(screen.getByRole("button", { name: "复制" }));
  expect(await screen.findByRole("status")).toHaveTextContent("复制失败，请手动选择");
  expect(window.getSelection()?.toString()).toBe("select-this-secret");
});
