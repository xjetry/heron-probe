import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { AdminService, SecurityActionKind } from "../gen/heron/v1/admin_pb";
import { renderWithAdmin } from "../test/harness";
import { Security } from "./Security";

describe("Security", () => {
  it("TOTP 启用后保留一次性恢复码，不再查询已撤销的会话", async () => {
    const getSecurity = vi.fn(async () => ({ totpEnabled: false, passkeyAvailable: true, passkeys: [], recoveryCodesRemaining: 0 }));
    const securityAction = vi.fn(async (request: { action: SecurityActionKind }) => {
      if (request.action === SecurityActionKind.TOTP_BEGIN) return { challengeId: "setup", totpSecret: "SECRET", totpUri: "otpauth://totp/probe" };
      if (request.action === SecurityActionKind.TOTP_ENABLE) return { recoveryCodes: ["RECOVERY-ONE", "RECOVERY-TWO"] };
      throw new Error("unexpected action");
    });
    const { queryClient, router } = renderWithAdmin({ getSecurity, securityAction }, [{ path: "/security/credentials", Component: Security }, { path: "/login", element: <h1>login</h1> }], "/security/credentials");
    await waitFor(() => expect(screen.getByRole("button", { name: "设置 TOTP" })).toBeEnabled());
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: "设置 TOTP" }));
    expect(await screen.findByText("SECRET")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("新认证器验证码"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "验证并启用" }));
    expect(await screen.findByText(/RECOVERY-ONE/)).toHaveTextContent("RECOVERY-TWO");
    expect(securityAction).toHaveBeenLastCalledWith(expect.objectContaining({ action: SecurityActionKind.TOTP_ENABLE, challengeId: "setup", otp: "123456" }), expect.anything());
    // useQuery 在 effect 里才把 enabled: !changed 交给 observer，恢复码进 DOM 时查询可能仍是启用的。要钉的是停用之后
    // 不再查询，所以先等到停用，再用失效触发一次本会发起的重取。
    const securityKey = createConnectQueryKey({ schema: AdminService.method.getSecurity, cardinality: "finite" });
    await waitFor(() => expect(queryClient.getQueryCache().findAll({ queryKey: securityKey }).map((q) => q.isDisabled())).toEqual([true]));
    const calls = getSecurity.mock.calls.length;
    await queryClient.invalidateQueries();
    expect(getSecurity).toHaveBeenCalledTimes(calls);
    expect(router.state.location.pathname).toBe("/security/credentials");
    expect(screen.getByRole("link", { name: "重新登录" })).toBeInTheDocument();
  });
});
