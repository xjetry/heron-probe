// 实时视图靠轮询（§10 默认 2 秒）；hub 的上报间隔不会更短，2 秒是让"刚上报"尽快可见的取值。
export const POLL_MS = 2000;

// 详情的周期量、诊断与对比页的 hub 时钟用较低频率读取，不依赖历史落盘刷出。
export const TRAFFIC_MS = 10_000;
