import { type ReactNode } from "react";
import { InstallCommands } from "./InstallCommands";
import { Modal } from "./Modal";
import { Secret } from "./Secret";

// 创建节点与换 token 后的一次性凭据：token 与用它拼出的安装命令一起放在弹窗里，节点列表再长也不会把它
// 顶出视口。token 明文只在这一次响应里出现，hub 只存哈希；关闭后不能再看到，需要时重新换发。
export function NodeInstallModal({ secretLabel, token, hubVersion, banner, opener, onClose }: {
  secretLabel: string; token: string; hubVersion?: string; banner?: ReactNode; opener: HTMLElement; onClose: () => void;
}) {
  return <Modal title="节点凭据" description="关闭后无法再看到明文 token，hub 只存哈希。" opener={opener} onClose={onClose}>
    <div className="modal-body">
      <Secret label={secretLabel} value={token} />
      <p>在被监控的机器上以 root 执行（agent 若经其他地址访问 hub，把命令里的地址换掉）：</p>
      {hubVersion === undefined
        ? <p className="muted">正在读取 hub 版本，读到后即可复制安装命令…</p>
        : <InstallCommands hubVersion={hubVersion} origin={window.location.origin} registerKey={token} banner={banner} />}
    </div>
    <footer className="modal-footer"><button type="button" className="primary-button" onClick={onClose}>完成</button></footer>
  </Modal>;
}
