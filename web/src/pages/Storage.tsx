import { useQuery } from "@connectrpc/connect-query";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type SeriesTableHealth } from "../gen/probe/v1/admin_pb";
import { bytes, duration } from "../lib/format";

const at = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString();

// 读数缺失的两种含义不同：表为空（没有最老桶），与本级没有水位（1m 级不经上卷写入）。
function Oldest({ h }: { h: SeriesTableHealth }) {
  if (h.oldestTs === undefined) return <span className="muted">空表</span>;
  // 是否标红只看 hub 给的结论：阈值在 hub 一处判定（store.SeriesHealth.Staleness），这里不重算。
  return h.oldestStale ? <span className="error">{at(h.oldestTs)}（超出保留期）</span> : <>{at(h.oldestTs)}</>;
}

function Watermark({ h }: { h: SeriesTableHealth }) {
  if (h.watermarkTs === undefined) return <span className="muted">—</span>;
  return h.watermarkStale ? <span className="error">{at(h.watermarkTs)}（上卷滞后）</span> : <>{at(h.watermarkTs)}</>;
}

// 完成时刻只在整轮成功时记下：缺失即从未成功跑过。
const finished = (unix: bigint | undefined) => (unix === undefined ? <span className="muted">从未成功</span> : at(unix));

export function Storage() {
  const stats = useQuery(AdminService.method.getStorageStats, {});
  const gate = queryGate(stats);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const s = gate.data;
  return (
    <section>
      {gate.banner}
      <h1>存储</h1>
      <p>数据库逻辑大小：{bytes(s.dbBytes)}</p>
      <p>
        上次清理完成：{finished(s.lastPruneAt)}；上次上卷完成：{finished(s.lastRollupAt)}
      </p>
      <p className="muted">
        清理停了只表现为库慢慢变大，上卷停了只表现为长窗口的图变空。最老桶对照保留期，标红表示超期的行没有被清掉（细一级只清理已上卷的部分，上卷停了它也会标红）；水位对照当前时刻，标红表示上卷停了，新库第一轮上卷之前也会标红约一分钟。
      </p>
      <div className="table-scroll" role="region" aria-label="时序表健康" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>表</th><th>桶长</th><th>保留期</th><th>最老桶</th><th>上卷水位</th></tr></thead>
          <tbody>
            {s.series.map((h) => (
              <tr key={h.table}>
                <td>{h.table}</td>
                <td>{duration(h.bucketS)}</td>
                <td>{duration(h.retentionS)}</td>
                <td><Oldest h={h} /></td>
                <td><Watermark h={h} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="table-scroll" role="region" aria-label="各表行数" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>表</th><th>行数</th></tr></thead>
          <tbody>
            {s.tables.map((t) => <tr key={t.name}><td>{t.name}</td><td>{String(t.rows)}</td></tr>)}
          </tbody>
        </table>
      </div>
    </section>
  );
}
