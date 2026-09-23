import { act } from "react";
import { expect, test } from "vitest";

test("入口将 React 应用挂载到 root 并移除原有内容", async () => {
  const root = document.createElement("div");
  root.id = "root";
  root.textContent = "尚未挂载";
  document.body.append(root);

  await act(async () => {
    await import("./main.tsx");
  });

  expect(root).toBeEmptyDOMElement();
});
