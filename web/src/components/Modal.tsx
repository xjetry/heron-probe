import { type ReactNode, useEffect, useId, useRef } from "react";
import { Icon } from "./Icon";

// 鼠标激活按钮不保证它获得焦点；调用方显式传入触发器，原生 dialog 负责背景 inert 与焦点约束。
export function Modal({ title, description, busy = false, onClose, children, className = "", opener }: {
  title: string; description?: string; busy?: boolean; onClose: () => void; children: ReactNode; className?: string; opener: HTMLElement;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    dialog.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    return () => {
      dialog.close();
      if (opener.isConnected) opener.focus({ preventScroll: true });
    };
  }, [opener]);
  return <dialog ref={ref} className={`admin-modal ${className}`} aria-labelledby={`${id}-title`} aria-describedby={description ? `${id}-description` : undefined}
    onCancel={(event) => { event.preventDefault(); if (!busy) onClose(); }}>
    <header className="modal-header">
      <div><h2 id={`${id}-title`}>{title}</h2>{description && <p id={`${id}-description`}>{description}</p>}</div>
      <button type="button" className="icon-button" aria-label="关闭弹窗" disabled={busy} onClick={onClose}><Icon name="close" /></button>
    </header>
    {children}
  </dialog>;
}
