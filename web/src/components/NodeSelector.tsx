import { useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import { AdminService, type Node } from "../gen/heron/v1/admin_pb";
import { Picks } from "./Picks";

export type NodeSelection = { allNodes: boolean; nodeIds: Set<bigint>; selectorTags: string[]; dynamic: boolean };

export function NodeSelector({ nodes, value, onChange, legend }: {
  nodes: Node[]; value: NodeSelection; onChange: (patch: Partial<NodeSelection>) => void; legend: string;
}) {
  const tags = useQuery(AdminService.method.listTags, {});
  const [filter, setFilter] = useState<string[]>([]);
  const available = [...new Set([...(tags.data?.tags.map((t) => t.name) ?? nodes.flatMap((n) => n.tags)), ...value.selectorTags])].sort();
  const selected = value.dynamic ? value.selectorTags : filter;
  const matching = nodes.filter((n) => selected.every((tag) => n.tags.includes(tag)));
  return <>
    <label className="inline"><input type="checkbox" checked={value.allNodes} onChange={(e) => onChange({ allNodes: e.target.checked })} />全部节点（含以后新建的节点）</label>
    {!value.allNodes && <>
      <label className="inline"><input type="checkbox" checked={value.dynamic} onChange={(e) => onChange({ dynamic: e.target.checked })} />动态标签选择器</label>
      <label>{value.dynamic ? "动态匹配标签（交集）" : "按标签筛选（交集）"}
        <select multiple required={value.dynamic} value={selected} onChange={(e) => {
          const next = [...e.target.selectedOptions].map((option) => option.value);
          if (value.dynamic) onChange({ selectorTags: next }); else setFilter(next);
        }}>
          {available.map((tag) => <option key={tag} value={tag}>{tag}</option>)}
        </select>
      </label>
      {value.dynamic ? <p className="muted">必须至少选择一个标签。当前匹配 {selected.length ? matching.length : 0} 个节点；以后新节点或标签变更会自动改变覆盖，移除标签也会撤销覆盖。</p>
        : <>
          <div className="row"><button type="button" onClick={() => onChange({ nodeIds: new Set([...value.nodeIds, ...matching.map((n) => n.id)]) })}>选择筛选结果（{matching.length}）</button>
            <button type="button" className="link" onClick={() => onChange({ nodeIds: new Set() })}>清空选择</button></div>
          <p className="muted">只保存本次选中的节点，之后标签变化不会改变分配。已选择 {value.nodeIds.size} 个节点。</p>
          <Picks legend={legend} items={nodes} selected={value.nodeIds} onChange={(nodeIds) => onChange({ nodeIds })} />
        </>}
    </>}
  </>;
}
