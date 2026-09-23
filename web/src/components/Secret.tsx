import { useState } from "react";

// token 与注册 key 只在创建那一次返回；hub 只存哈希，离开这个页面就再也看不到。
export function Secret({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
  };
  return (
    <div className="card">
      <p><strong>{label}</strong> —— 只显示这一次，hub 不保存明文。</p>
      <code className="secret" aria-label={label}>{value}</code>
      <p>
        <button type="button" onClick={copy}>{copied ? "已复制" : "复制"}</button>
      </p>
    </div>
  );
}
