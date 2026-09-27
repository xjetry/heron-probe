import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";

// hub 签发或清除请求者自己的会话 cookie 时（Login、Logout、撤销当前会话），前端缓存必须
// 跟着清空：react-query 缓存跨会话存活会让登录页短暂展示上一个会话取到的数据。跳转用
// replace 而非 push，回退键不该把用户带回一个凭据已失效的会话页面。
export function useLeaveSession(): () => void {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return () => {
    queryClient.clear();
    void navigate("/login", { replace: true });
  };
}
