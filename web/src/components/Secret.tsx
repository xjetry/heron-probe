import { useEffect, useRef, useState } from "react";

// token 与注册 key 只在创建那一次返回；hub 只存哈希，离开这个页面就再也看不到。
export function Secret({ label, value }: { label: string; value: string }) {
  const code = useRef<HTMLElement>(null);
  const [result, setResult] = useState<"idle" | "copied" | "failed">("idle");
  const generation = useRef(0);
  useEffect(() => {
    setResult("idle");
    // 复制可能晚于凭据切换结束，过期结果不能改变新凭据的反馈或选区。
    return () => { generation.current++; };
  }, [value]);
  const copy = async () => {
    const attempt = generation.current;
    let next: "copied" | "failed";
    try {
      if (!navigator.clipboard) throw new Error("clipboard unavailable");
      await navigator.clipboard.writeText(value);
      next = "copied";
    } catch {
      next = "failed";
    }
    if (attempt !== generation.current) return;
    setResult(next);
    if (next === "failed" && code.current) {
      const range = document.createRange();
      range.selectNodeContents(code.current);
      const selection = window.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
    }
  };
  return (
    <div className="card">
      <p><strong>{label}</strong> —— 只显示这一次，hub 不保存明文。</p>
      <code ref={code} className="secret" aria-label={label}>{value}</code>
      <p>
        <button type="button" onClick={copy}>{result === "copied" ? "已复制" : "复制"}</button>
        <span role="status">{result === "failed" ? "复制失败，请手动选择" : ""}</span>
      </p>
    </div>
  );
}
