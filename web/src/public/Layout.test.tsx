import { Code, ConnectError } from "@connectrpc/connect";
import { screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { renderWithService } from "../test/harness";
import { PublicLayout } from "./Layout";
import { DEFAULT_TITLE } from "./site";

afterEach(() => { document.title = ""; });

// 取不到站点设置（这里是被限流）时按全部为空处理：标签页标题与页头同为内置标题。
it("站点设置取不到时标签页标题与页头一致", async () => {
  renderWithService(PublicService, { getSite: async () => { throw new ConnectError("rate limit exceeded", Code.ResourceExhausted); } },
    [{ path: "/", Component: PublicLayout }], "/");
  expect(await screen.findByRole("alert")).toHaveTextContent("rate limit exceeded");
  expect(screen.getByRole("link", { name: DEFAULT_TITLE })).toBeInTheDocument();
  await waitFor(() => expect(document.title).toBe(DEFAULT_TITLE));
});
