import { useRef, useState } from "react";
import { Modal } from "./Modal";
// 脚本模板随面板打包进 hub 二进制，与 hub 同版本（同 SKILL.md 下发入口卡片的理由：面板不另存一份）。
import template from "../assets/heron-quick-node.user.js?raw";

// 复制时替换两个占位符：hub 地址取浏览器当前 origin（与注册窗口安装命令同一口径，agent 经另一地址访问
// hub 时用户在脚本里改），token 只在刚创建、明文还在手上时填入；否则留空，脚本首次运行会引导设置。
export function buildUserscript(origin: string, token: string): string {
  return template.replaceAll("__HERON_HUB__", origin).replaceAll("__HERON_TOKEN__", token);
}

// 复制油猴脚本到剪贴板；剪贴板不可用（权限、非安全上下文）时退回弹窗手动全选。
export function UserscriptButton({ token = "", label = "复制油猴脚本" }: { token?: string; label?: string }) {
  const button = useRef<HTMLButtonElement>(null);
  const [state, setState] = useState<"idle" | "copied">("idle");
  const [manual, setManual] = useState(false);
  const source = buildUserscript(window.location.origin, token);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(source);
      setState("copied");
      setTimeout(() => setState("idle"), 1500);
    } catch {
      setManual(true);
    }
  };
  return <>
    <button ref={button} type="button" onClick={() => void copy()}>
      {state === "copied" ? "已复制" : label}
    </button>
    {manual && button.current && <Modal title={label} description="浏览器拒绝了剪贴板访问，请全选复制" onClose={() => setManual(false)} opener={button.current}>
      <textarea data-autofocus readOnly rows={12} value={source} aria-label="油猴脚本源码"
        onFocus={(event) => event.currentTarget.select()}
        style={{ width: "100%", fontFamily: "monospace", fontSize: "12px" }} />
    </Modal>}
  </>;
}
