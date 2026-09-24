import { useState } from "react";

// 危险操作两段式确认：首击只进入确认态，确认才执行；执行挂起时禁用确认，双击不会重复提交。
export function ConfirmDelete({ label, confirm, note, pending, onDelete }: {
  label: string; confirm: string; note?: string; pending: boolean; onDelete: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  if (!confirming) {
    return <button type="button" className="link danger" aria-label={label} onClick={() => setConfirming(true)}>删除</button>;
  }
  return (
    <>
      <button type="button" className="danger" disabled={pending} onClick={onDelete}>{confirm}</button>{" "}
      {note ? <><span className="muted">{note}</span>{" "}</> : null}
      <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
    </>
  );
}
