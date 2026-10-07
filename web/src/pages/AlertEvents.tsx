import { useQuery } from "@connectrpc/connect-query";
import { Link, useSearchParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService } from "../gen/heron/v1/admin_pb";
import { EventFeed, useAlertEvents } from "../components/EventFeed";
import { withId } from "../lib/ids";

export function AlertEvents() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("node");
  // 无效参数不能退化成"全部节点"——那是放宽；直接报错，不发请求。
  const valid = raw === null || /^[1-9]\d*$/.test(raw);
  const nodeId = raw !== null && valid ? BigInt(raw) : 0n;
  const nodes = useQuery(AdminService.method.listNodes, {});
  const channels = useQuery(AdminService.method.listNotifyChannels, {});
  const events = useAlertEvents(valid ? nodeId : null);
  if (!valid) return <p role="alert" className="error">节点参数 {raw} 无效。<Link to="/events">查看全部事件</Link></p>;
  // 外壳（标题、筛选、节点名）只依赖节点列表；事件列表是页内区域，区域未就绪不卸载外壳与筛选焦点。
  const shell = queryGate(nodes);
  if (!shell.ready) return shell.loading ?? errorBanner(...shell.errors);
  const nodeList = shell.data.nodes;
  const nodeName = (id: bigint) => id === 0n ? "系统" : nodeList.find((n) => n.id === id)?.name ?? `节点 #${id}`;
  // 渠道只提供名称：它的失败只进横幅，不阻断事件；名称缺失时按编号回退。
  const channelName = (id: bigint) => channels.data?.channels.find((c) => c.id === id)?.name ?? `渠道 #${id}`;
  return (
    <section>
      <h1>告警事件</h1>
      <label>节点
        <select value={String(nodeId)} onChange={(e) => setParams(e.target.value === "0" ? {} : { node: e.target.value })}>
          <option value="0">全部节点</option>
          {nodeList.map((n) => <option key={String(n.id)} value={String(n.id)}>{withId(n.name, n.id)}</option>)}
          {nodeId !== 0n && !nodeList.some((n) => n.id === nodeId) && <option value={String(nodeId)}>{nodeName(nodeId)}</option>}
        </select>
      </label>
      {errorBanner(nodes.error, events.error, channels.error)}
      <EventFeed events={events} nodeName={nodeName} channelName={channelName} />
    </section>
  );
}
