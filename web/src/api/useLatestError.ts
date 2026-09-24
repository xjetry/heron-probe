import { useRef, useState } from "react";

// 多个 mutation 共用一处提示；较早操作的迟到失败不能盖过用户后来发起的操作。
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
