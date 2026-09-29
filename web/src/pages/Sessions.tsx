import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLeaveSession } from "../api/useLeaveSession";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { Link } from "react-router";

export function Sessions() {
  const queryClient = useQueryClient();
  const leaveSession = useLeaveSession();
  const list = useQuery(AdminService.method.listSessions, {});
  const revoke = useMutation(AdminService.method.revokeSession);
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const remove = (id: string, current: boolean) => revoke.mutate({ id }, {
    onSuccess: () => {
      if (current) {
        leaveSession();
      } else {
        void queryClient.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listSessions, cardinality: "finite" }) });
      }
    },
  });
  return (
    <section>
      {gate.banner}
      <h1>安全</h1>
      <p><Link to="/security/credentials">管理 TOTP、恢复码与 Passkey</Link></p>
      <p className="muted">有效登录会话。撤销后，该会话立即失效；撤销当前会话会返回登录页。</p>
      {revoke.error && <p role="alert" className="error">{errorText(revoke.error)}</p>}
      <div className="table-scroll" role="region" aria-label="登录会话" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>会话</th><th>创建于</th><th>最近使用</th><th>操作</th></tr></thead>
          <tbody>
            {gate.data.sessions.map((session) => (
              <tr key={session.id}>
                <td><code title={session.id}>{session.id.slice(0, 12)}</code>{session.current && <> <span className="muted">当前</span></>}</td>
                <td>{new Date(Number(session.createdAt) * 1000).toLocaleString()}</td>
                <td>{new Date(Number(session.lastUsedAt) * 1000).toLocaleString()}</td>
                <td>
                  <ConfirmDelete label={`撤销会话 ${session.id.slice(0, 12)}`} confirm={`确认撤销会话 ${session.id.slice(0, 12)}`} verb="撤销"
                    note={session.current ? "将退出当前登录" : "该会话将立即失效"}
                    pending={revoke.isPending} onDelete={() => remove(session.id, session.current)} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {gate.data.sessions.length === 0 && <p className="muted">没有有效会话。</p>}
    </section>
  );
}
