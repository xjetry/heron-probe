import { useQuery } from "@connectrpc/connect-query";
import { useMemo } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { ProbeComparison, type ProbeComparisonMethods } from "../components/ProbeComparison";
import { AdminService } from "../gen/heron/v1/admin_pb";
import { taskIdParam } from "../lib/probeComparison";

const METHODS: ProbeComparisonMethods = {
  list: AdminService.method.listProbeComparisonNodes,
  query: AdminService.method.queryProbeComparison,
};

export function ProbeCompare() {
  const { id } = useParams();
  const taskId = taskIdParam(id);
  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: taskId !== undefined });
  const named = useMemo(() => (nodes.data?.nodes ?? []).map((node) => ({ id: node.id, name: node.name })), [nodes.data]);
  if (taskId === undefined) return <p role="alert" className="error">任务编号无效。</p>;
  const gate = queryGate(nodes);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  return (
    <>
      <p><Link to="/probes">返回探测任务</Link></p>
      <ProbeComparison taskId={taskId} methods={METHODS} nodes={named} />
    </>
  );
}
