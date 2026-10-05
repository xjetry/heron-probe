import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { InstallCommands } from "../components/InstallCommands";
import { Secret } from "../components/Secret";
import { AdminService } from "../gen/heron/v1/admin_pb";

const TTLS = [
  { label: "10 分钟", seconds: 600 },
  { label: "1 小时", seconds: 3600 },
  { label: "24 小时", seconds: 86400 },
  { label: "7 天", seconds: 7 * 86400 },
];

export function RegisterWindow() {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getRegisterWindow, cardinality: "finite" }) });
  const status = useQuery(AdminService.method.getRegisterWindow, {}, { refetchInterval: 10_000 });
  // hub 版本在进程生命周期内不变，不轮询。
  const snapshot = useQuery(AdminService.method.getSnapshot, {});
  const [ttl, setTtl] = useState(TTLS[1].seconds);
  const [maxNodes, setMaxNodes] = useState(5);
  const [key, setKey] = useState<string | null>(null);
  const clearKey = useCallback(() => setKey(null), []);
  useEffect(() => {
    if (status.data?.open === false) clearKey();
  }, [status.data?.open, clearKey]);
  // 开窗与关窗是操作，错误生命周期独立于窗口状态的轮询。
  const { error, mutationOptions } = useLatestError();
  const open = useMutation(AdminService.method.openRegisterWindow, {
    ...mutationOptions,
    onSuccess: (r) => { setKey(r.key); void refresh(); },
  });
  const close = useMutation(AdminService.method.closeRegisterWindow, {
    ...mutationOptions,
    onSuccess: () => { clearKey(); void refresh(); },
  });
  const onOpen = (e: FormEvent) => { e.preventDefault(); open.mutate({ ttlS: ttl, maxNodes }); };
  const gate = queryGate(status);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  // 命令区域只依赖快照；外壳（key、开窗表单）不等它。未就绪时不拼命令：用空版本先渲染 latest 命令
  // 再在快照到达后变成钉版本的命令，会让先复制的人装上与 hub 不同的版本。
  const snap = queryGate(snapshot);
  return (
    <section>
      <h1>注册窗口</h1>
      {gate.data.open ? (
        <p>窗口开启中：剩余 {gate.data.remaining} 个名额，截止 {new Date(Number(gate.data.expiresAt) * 1000).toLocaleString()}。{" "}
          <button type="button" className="danger" onClick={() => close.mutate({})} disabled={close.isPending}>关闭窗口</button>
        </p>
      ) : (
        <p className="muted">当前没有开启的窗口。</p>
      )}
      {key && (
        <>
          <Secret label="注册 key" value={key} />
          <p>在被监控的机器上以 root 执行（agent 若经其他地址访问 hub，把命令里的地址换掉）：</p>
          {snap.ready ? (
            <InstallCommands hubVersion={snap.data.hubVersion} boundAgentVersion={snap.data.boundAgentVersion} origin={window.location.origin} registerKey={key} banner={snap.banner} />
          ) : (
            snap.loading ?? errorBanner(...snap.errors)
          )}
        </>
      )}
      <form onSubmit={onOpen} className="row">
        <label>有效期
          <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
            {TTLS.map((t) => <option key={t.seconds} value={t.seconds}>{t.label}</option>)}
          </select>
        </label>
        <label>可注册节点数<input type="number" min={1} max={1000} value={Number.isNaN(maxNodes) ? "" : maxNodes} onChange={(e) => setMaxNodes(e.target.valueAsNumber)} /></label>
        <button type="submit" disabled={open.isPending || !(maxNodes >= 1)}>开启新窗口</button>
      </form>
      {gate.banner}
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
    </section>
  );
}
