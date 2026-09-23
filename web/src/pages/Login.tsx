import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { errorText } from "../api/auth";

export function Login() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const login = useMutation(AdminService.method.login, {
    onSuccess: () => { queryClient.clear(); void navigate("/", { replace: true }); },
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
