import { Code, ConnectError } from "@connectrpc/connect";

// 会话过期、被改密码登出、从未登录，对前端是同一件事：回到登录页。
export function isUnauthenticated(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Unauthenticated;
}
