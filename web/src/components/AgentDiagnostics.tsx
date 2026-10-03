import { CollectionComponent, type AgentDiagnostics as Diagnostics } from "../gen/heron/v1/types_pb";

const COLLECTORS: Record<CollectionComponent, string> = {
  [CollectionComponent.UNSPECIFIED]: "未知类别",
  [CollectionComponent.BOOT_ID]: "启动标识",
  [CollectionComponent.CPU]: "CPU",
  [CollectionComponent.MEMORY]: "内存",
  [CollectionComponent.SWAP]: "交换空间",
  [CollectionComponent.DISK]: "磁盘",
  [CollectionComponent.LOAD]: "负载",
  [CollectionComponent.PROCS]: "进程数",
  [CollectionComponent.UPTIME]: "运行时间",
  [CollectionComponent.CONNS]: "连接数",
  [CollectionComponent.NET]: "网络",
  [CollectionComponent.DISK_IO]: "磁盘 I/O",
};

export function AgentDiagnostics({ diagnostics, updatedAt }: { diagnostics?: Diagnostics; updatedAt?: bigint }) {
  const updated = updatedAt && updatedAt > 0n ? new Date(Number(updatedAt) * 1000) : undefined;
  const networkFailed = diagnostics?.failedCollectors.includes(CollectionComponent.NET);
  return <section className="card" aria-label="Agent 运行诊断">
    <h2>Agent 运行诊断</h2>
    {!diagnostics ? <p className="muted">Agent 未提供诊断信息，请更新 Agent 后等待上报。</p> : <>
      <p className="muted">最近保存的诊断，不保证实时健康。生效参数只读。</p>
      <dl className="facts">
        <dt>诊断信息更新时间</dt><dd>{updated ? <time dateTime={updated.toISOString()}>{updated.toLocaleString()}</time> : "未知"}</dd>
        <dt>生效上报间隔</dt><dd>{diagnostics.reportIntervalMs > 0 ? <>{diagnostics.reportIntervalMs} ms <span className="muted">（不含请求耗时和失败退避）</span></> : "未知"}</dd>
        <dt>包含规则</dt><dd>{diagnostics.netInclude.length ? <code>{diagnostics.netInclude.join(" ")}</code> : "全部接口"}</dd>
        <dt>排除规则</dt><dd>{diagnostics.netExclude.length ? <code>{diagnostics.netExclude.join(" ")}</code> : "无"}</dd>
        <dt>实际计入接口</dt><dd>{networkFailed ? <span className="warn">本次网络采集失败，接口清单不可用</span> : diagnostics.netInterfacesTotal === 0 ? "没有计入的接口" : <>
          <div className="muted">共 {diagnostics.netInterfacesTotal} 个{diagnostics.netInterfacesTotal > diagnostics.netInterfaces.length && `，仅展示前 ${diagnostics.netInterfaces.length} 个`}</div>
          {diagnostics.netInterfaces.map((name) => <code className="tag" key={name}>{name}</code>)}
        </>}</dd>
        <dt>采集失败项</dt><dd>{diagnostics.failedCollectors.length ? <span className="warn">{diagnostics.failedCollectors.map((component) => COLLECTORS[component] ?? "未知类别").join("、")}</span> : "最近采集未报告失败"}</dd>
      </dl>
    </>}
  </section>;
}
