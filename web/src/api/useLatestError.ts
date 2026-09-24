import { useRef, useState } from "react";

// 同一时刻只显示最新一次操作的失败；较早操作的迟到失败被丢弃——它的目标行可能已被后一操作改变，
// 且服务端错误文本自带字段名，用户重试即可再次看到。
export function useLatestError() {
  const latest = useRef(0);
  const [error, setError] = useState<unknown>(null);
  return {
    error,
    onMutate: () => { setError(null); return ++latest.current; },
    onError: (err: unknown, _variables: unknown, operation: number | undefined) => {
      if (operation === latest.current) setError(err);
    },
  };
}
