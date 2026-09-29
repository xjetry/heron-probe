import { Code, ConnectError } from "@connectrpc/connect";
import { screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { PublicService } from "../gen/heron/v1/public_pb";
import { renderWithService } from "../test/harness";
import { PublicLayout } from "./Layout";
import { DEFAULT_TITLE } from "./site";
import heron from "../assets/heron.svg";

afterEach(() => { document.title = ""; });

// 取不到站点设置（这里是被限流）时按全部为空处理：标签页标题与页头同为内置标题。
it("站点设置取不到时标签页标题与页头一致", async () => {
  renderWithService(PublicService, { getSite: async () => { throw new ConnectError("rate limit exceeded", Code.ResourceExhausted); } },
    [{ path: "/", Component: PublicLayout }], "/");
  expect(await screen.findByRole("alert")).toHaveTextContent("rate limit exceeded");
  expect(screen.getByRole("link", { name: DEFAULT_TITLE })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: DEFAULT_TITLE }).querySelector("img")).toHaveAttribute("src", heron);
  await waitFor(() => expect(document.title).toBe(DEFAULT_TITLE));
});

it("自定义站点标题与 logo 不被 Heron 默认品牌覆盖", async () => {
  renderWithService(PublicService, { getSite: async () => ({ title: "我的机房", logo: "https://example.com/custom.svg" }) },
    [{ path: "/", Component: PublicLayout }], "/");
  const brand = await screen.findByRole("link", { name: "我的机房" });
  expect(brand.querySelectorAll("img")).toHaveLength(1);
  expect(brand.querySelector("img")).toHaveAttribute("src", "https://example.com/custom.svg");
  await waitFor(() => expect(document.title).toBe("我的机房"));
});
