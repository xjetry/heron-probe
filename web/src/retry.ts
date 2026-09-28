import { Code, ConnectError } from "@connectrpc/connect";

// 查询失败后只对可能自行恢复的错误重试，面板与公开页两个入口共用。写成白名单：以后新出现的码默认不重试，重试不会把
// 确定性的失败变成成功，只会让页面晚几秒才显示同一个结果（主题页未配 --theme-origin 时的说明要等完两次退避才出现）。
//
// connect-es 2.2.0 的传输层映射：fetch 本身失败（网络断开、连接被拒）得到 Unknown；没有 Connect 错误正文的 HTTP 502、
// 503、504 与 429（反代或网关的瞬时错误页）得到 Unavailable，HTTP 500 得到 Unknown。Unavailable、DeadlineExceeded、Aborted
// 本身就是"稍后再试"的语义。connect-go 对没有码的错误同样回 Unknown，与网络失败共用一个码、分不开，一并重试。
export const retryableCodes: ReadonlySet<Code> = new Set([Code.Unavailable, Code.DeadlineExceeded, Code.Aborted, Code.Unknown]);

// 不重试的码：配置态（FailedPrecondition，例如未配 --theme-origin）、调用方的错与权限（InvalidArgument、NotFound、
// AlreadyExists、PermissionDenied、Unauthenticated——会话不会因重试复活）、服务端的确定性失败（Internal、Unimplemented、
// DataLoss、OutOfRange）、取消（Canceled：页面自己放弃了这次请求），以及 ResourceExhausted（AdminService 用它报主题、
// token、节点等的数量上限，重试腾不出名额；PublicService 用它报按来源计的限流，一两秒内重试只会再耗同一个桶的令牌）。
// 只供枚举用例核对每个码都被有意归了类，谓词只看 retryableCodes。
export const nonRetryableCodes: ReadonlySet<Code> = new Set([
  Code.Canceled,
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.PermissionDenied,
  Code.ResourceExhausted,
  Code.FailedPrecondition,
  Code.OutOfRange,
  Code.Unimplemented,
  Code.Internal,
  Code.DataLoss,
  Code.Unauthenticated,
]);

// retryQuery 是面板与公开页查询的重试谓词（TanStack Query 的 retry）：可重试的错误至多再试两次，其余不重试。不是
// ConnectError 的异常来自页面自己的代码，同样不重试。
export function retryQuery(failureCount: number, err: unknown): boolean {
  return failureCount < 2 && err instanceof ConnectError && retryableCodes.has(err.code);
}
