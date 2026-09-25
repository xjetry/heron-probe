import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { Secret } from "../components/Secret";
import { AdminService } from "../gen/probe/v1/admin_pb";

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
          <p>在被监控的机器上执行：</p>
          <pre className="secret">{`probe-agent register --hub ${window.location.origin} --key ${key}`}</pre>
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
