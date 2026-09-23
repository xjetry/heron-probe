import { useRef, useState } from "react";

// token 与注册 key 只在创建那一次返回；hub 只存哈希，离开这个页面就再也看不到。
export function Secret({ label, value }: { label: string; value: string }) {
  const code = useRef<HTMLElement>(null);
  const [result, setResult] = useState<"idle" | "copied" | "failed">("idle");
  const copy = async () => {
    try {
      if (!navigator.clipboard) throw new Error("clipboard unavailable");
      await navigator.clipboard.writeText(value);
      setResult("copied");
    } catch {
      setResult("failed");
      if (code.current) {
        const range = document.createRange();
        range.selectNodeContents(code.current);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
      }
    }
  };
  return (
    <div className="card">
      <p><strong>{label}</strong> —— 只显示这一次，hub 不保存明文。</p>
      <code ref={code} className="secret" aria-label={label}>{value}</code>
      <p>
        <button type="button" onClick={copy}>{result === "copied" ? "已复制" : "复制"}</button>
        {result === "failed" && <span role="status">复制失败，请手动选择</span>}
      </p>
    </div>
  );
}
