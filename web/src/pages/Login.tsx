import { ConnectError } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { type FormEvent, useState } from "react";
import { useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export function Login() {
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const login = useMutation(AdminService.method.login, {
    onSuccess: () => void navigate("/", { replace: true }),
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ password });
  };
  return (
    <main className="login">
      <form onSubmit={onSubmit} className="card">
        <h1>probe</h1>
        <label>
          管理员密码
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoFocus
          />
        </label>
        <button type="submit" disabled={login.isPending || password === ""}>
          登录
        </button>
        {login.error && (
          <p role="alert" className="error">
            {errorText(login.error)}
          </p>
        )}
      </form>
    </main>
  );
}

// hub 的错误信息本身就是给人读的；这里只去掉 Connect 的错误码前缀。
export function errorText(err: unknown): string {
  return err instanceof ConnectError ? err.rawMessage : String(err);
}
