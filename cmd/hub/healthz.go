package main

import "net/http"

// healthzHandler 是 /healthz 的处理器：无鉴权、只回 "ok\n"，不输出任何版本、节点数、配置或计数。
//
// 挂载时机是一条不变式，由 cmd/hub/serve.go 保证：newHandler 在 store.Open 打开库、全部 Load 加载内存索引
// 并构造完所有服务之后才把这个处理器放进 mux，而这个 mux 只在 srv.Serve(listener) 里才开始被调用。§5.7 的
// 在线更新事务要求候选 hub "绑定监听但在确认前不开始 Serve"，所以 net.Listen 之后、Serve 之前的连接只进内核
// 队列、得不到应答；health 因此在候选被确认之前不会报就绪，与 Serve 同生命周期，不会在绑定即应答。
//
// 它是普通 HTTP 路径，不是 PublicService 的挂载点，所以不进公开服务的限流桶（§10 只覆盖 PublicService 挂载点）。
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
