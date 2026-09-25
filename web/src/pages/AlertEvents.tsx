import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import { skipToken, type InfiniteData } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type ListAlertEventsResponse } from "../gen/probe/v1/admin_pb";
import { deliveryText, transitionLabel } from "../lib/alerts";

// 与 hub 的默认页长一致；不足一页即已到最早的事件。
const PAGE = 100;

export function AlertEvents() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("node");
  // 无效参数不能退化成"全部节点"——那是放宽；直接报错，不发请求。
  const valid = raw === null || /^[1-9]\d*$/.test(raw);
  const nodeId = raw !== null && valid ? BigInt(raw) : 0n;
  const nodes = useQuery(AdminService.method.listNodes, {});
  const channels = useQuery(AdminService.method.listNotifyChannels, {});
  const events = useInfiniteQuery(AdminService.method.listAlertEvents, valid ? { nodeId, beforeId: 0n, limit: PAGE } : skipToken, {
    pageParamKey: "beforeId",
    // 事件按 id 倒序；下一页从本页最小 id 之前开始。
    getNextPageParam: (last) => (last.events.length < PAGE ? undefined : last.events[last.events.length - 1].id),
  });
  if (!valid) return <p role="alert" className="error">节点参数 {raw} 无效。<Link to="/events">查看全部事件</Link></p>;
  // 外壳（标题、筛选、节点名）只依赖节点列表；事件列表是页内区域，区域未就绪不卸载外壳与筛选焦点。
  const shell = queryGate(nodes);
  if (!shell.ready) return shell.loading ?? errorBanner(...shell.errors);
  const nodeList = shell.data.nodes;
  const nodeName = (id: bigint) => nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  // 渠道只提供名称：它的失败只进横幅，不阻断事件；名称缺失时按编号回退。
  const channelName = (id: bigint) => channels.data?.channels.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  const region = queryGate(events);
  return (
    <section>
      <h1>告警事件</h1>
      <label>节点
        <select value={String(nodeId)} onChange={(e) => setParams(e.target.value === "0" ? {} : { node: e.target.value })}>
          <option value="0">全部节点</option>
          {nodeList.map((n) => <option key={String(n.id)} value={String(n.id)}>{n.name}</option>)}
          {nodeId !== 0n && !nodeList.some((n) => n.id === nodeId) && <option value={String(nodeId)}>{nodeName(nodeId)}</option>}
        </select>
      </label>
      {errorBanner(nodes.error, events.error, channels.error)}
      {region.ready ? (
        <EventList data={region.data} nodeName={nodeName} channelName={channelName}
          hasNextPage={events.hasNextPage} fetchingNext={events.isFetchingNextPage} onMore={() => void events.fetchNextPage()} />
      ) : (
        region.loading
      )}
    </section>
  );
}

function EventList({ data, nodeName, channelName, hasNextPage, fetchingNext, onMore }: {
  data: InfiniteData<ListAlertEventsResponse>; nodeName: (id: bigint) => string; channelName: (id: bigint) => string;
  hasNextPage: boolean; fetchingNext: boolean; onMore: () => void;
}) {
  const rows = data.pages.flatMap((p) => p.events);
  return (
    <>
      <div className="table-scroll" role="region" aria-label="告警事件" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>时间</th><th>节点</th><th>变化</th><th>摘要</th><th>通知</th></tr></thead>
          <tbody>
            {rows.map((ev) => (
              <tr key={String(ev.id)}>
                <td>{new Date(Number(ev.at) * 1000).toLocaleString()}</td>
                <td>{nodeName(ev.nodeId)}</td>
                <td className={ev.transition === "firing" ? "error" : undefined}>{transitionLabel(ev.transition)}</td>
                <td>{ev.summary}</td>
                <td>
                  {ev.deliveries.length === 0
                    ? <span className="muted">未配置渠道</span>
                    : ev.deliveries.map((d) => <div key={String(d.channelId)}>{deliveryText(d, channelName(d.channelId))}</div>)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {hasNextPage && (
        <button type="button" disabled={fetchingNext} onClick={onMore}>加载更早的事件</button>
      )}
      {rows.length === 0 && <p className="muted">没有告警事件。</p>}
    </>
  );
}
