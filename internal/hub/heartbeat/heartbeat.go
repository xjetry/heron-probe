// Package heartbeat 实现 hub 自身的心跳外推（§9.6）：按设置周期向一个外部监控服务发一次请求，hub 死了外部服务收不到
// 心跳就告警。默认关闭，目标只来自设置；出站复用 §9.3 的边界（outbound 客户端，不跟随重定向、去掉 URL 的错误文本）。
//
// 循环每轮重读设置，所以改间隔下一轮生效、清空 url 立即停发，都不需要重启。状态只在内存里：重启后"从未跑过"。
package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/outbound"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

// Timeout 是一次心跳外呼的总时限，覆盖建连、写请求与读完应答体。构造客户端时用它（outbound.NewClient(Timeout)），
// 使所有外呼共用同一个上界。
const Timeout = 10 * time.Second

// maxResponseBytes 是应答体的读取上限：心跳只关心状态码，接收方回多大的内容都不该被 hub 整读进内存。
const maxResponseBytes = 64 << 10

// 失败类别，复用 §9.3 的词汇；在产生处确定，不从错误文本反推。
const (
	CategoryTransport  = "transport"
	CategoryHTTPStatus = "http_status"
	CategoryRequest    = "request"
)

// Counts 是一次外呼携带的聚合计数。只放聚合值与版本（拓扑不发给第三方）。
type Counts struct {
	NodesTotal  int
	Online      int
	Offline     int
	Maintenance int
	Firing      int
}

// Source 是心跳循环的读侧：当前设置与聚合计数。装配方在各自入口完成 SQL 与在线判定（在线沿用 §4.4 的 live 判定），
// 心跳包不写 SQL、不重算宽限期。
type Source interface {
	HeartbeatSettings(ctx context.Context) (store.HeartbeatSettings, error)
	HeartbeatCounts(ctx context.Context) (Counts, error)
}

// Status 是进程内的心跳状态。时间都是墙钟；零值表示"从未跑过"。
type Status struct {
	LastSuccessAt     time.Time
	LastFailureAt     time.Time
	FailureCategory   string
	FailureHTTPStatus int
	NextAt            time.Time
}

// report 是 POST 正文。字段顺序固定，值只有聚合计数与版本，不含节点名或地址。
type report struct {
	NodesTotal  int    `json:"nodes_total"`
	Online      int    `json:"online"`
	Offline     int    `json:"offline"`
	Maintenance int    `json:"maintenance"`
	Firing      int    `json:"firing"`
	HubVersion  string `json:"hub_version"`
}

type Heartbeat struct {
	src     Source
	client  *http.Client
	version string
	clk     clock.Clock
	log     *slog.Logger
	// wait 是循环的等待出口：生产用定时器，测试替换它做确定性推进。返回 false 表示 ctx 已结束。
	wait func(ctx context.Context, d time.Duration) bool

	mu     sync.Mutex
	status Status
	// loggedFailure 是上一轮失败记日志时用的签名（类别与状态码）；成功清零。它让连续同一失败只记一行、状态变化立即
	// 再记。只在循环协程里读写。
	loggedFailure string
}

func New(src Source, client *http.Client, version string, clk clock.Clock, log *slog.Logger) *Heartbeat {
	if src == nil {
		panic("heartbeat.New: Source must be set")
	}
	if client == nil {
		panic("heartbeat.New: client must be set")
	}
	if clk == nil {
		panic("heartbeat.New: clock must be set")
	}
	if log == nil {
		panic("heartbeat.New: logger must be set")
	}
	return &Heartbeat{src: src, client: client, version: version, clk: clk, log: log, wait: waitReal}
}

// Status 返回当前进程内状态的一份拷贝；api 的 GetHeartbeatStatus 读它。
func (h *Heartbeat) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Run 启动即跑一轮，之后按每轮读到的 interval_s 等待。设置每轮重读：启动后配置立即生效，改间隔下一轮生效，清空 url
// 立即停发，都不需要重启。
func (h *Heartbeat) Run(ctx context.Context) {
	for {
		d := h.round(ctx)
		if !h.wait(ctx, d) {
			return
		}
	}
}

// round 跑一轮并返回这一轮之后要等待的时长。设置读不出时不外呼，按默认间隔重试。
func (h *Heartbeat) round(ctx context.Context) time.Duration {
	st, err := h.src.HeartbeatSettings(ctx)
	if err != nil {
		h.log.Error("reading heartbeat settings failed", "err", err)
		return store.DefaultHeartbeatIntervalS * time.Second
	}
	interval := time.Duration(st.IntervalS) * time.Second
	if st.URL != "" {
		h.send(ctx, st)
	}
	h.mu.Lock()
	h.status.NextAt = h.clk.Now().Add(interval)
	h.mu.Unlock()
	return interval
}

func (h *Heartbeat) send(ctx context.Context, st store.HeartbeatSettings) {
	method, ok := httpMethods[st.Method]
	if !ok {
		h.failure(CategoryRequest, 0, fmt.Errorf("unsupported heartbeat method %q", st.Method))
		return
	}
	var body []byte
	if method == http.MethodPost {
		counts, err := h.src.HeartbeatCounts(ctx)
		if err != nil {
			// 计数读不出是 hub 自己的库问题，不是一次外呼的失败：本轮不发，也不记成功或失败。
			h.log.Error("reading heartbeat counts failed", "err", err)
			return
		}
		if body, err = json.Marshal(report{
			NodesTotal: counts.NodesTotal, Online: counts.Online, Offline: counts.Offline,
			Maintenance: counts.Maintenance, Firing: counts.Firing, HubVersion: h.version,
		}); err != nil {
			h.log.Error("encoding heartbeat body failed", "err", err)
			return
		}
	}
	req, err := buildRequest(ctx, method, st.URL, body)
	if err != nil {
		h.failure(CategoryRequest, 0, err)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.failure(CategoryTransport, 0, err)
		return
	}
	defer resp.Body.Close()
	// 应答体只为连接复用而读，不超过 maxResponseBytes；它是 2xx 时不因读不出而改判——心跳已经送达。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		h.failure(CategoryHTTPStatus, resp.StatusCode, nil)
		return
	}
	h.success()
}

// buildRequest 复用 outbound 客户端的重定向禁令；URL 的合法性在产生处判定，错误文本不含 URL。
func buildRequest(ctx context.Context, method, rawURL string, body []byte) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return nil, fmt.Errorf("heartbeat target is not an absolute http(s) URL")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

var httpMethods = map[store.HeartbeatMethod]string{
	store.HeartbeatGet:  http.MethodGet,
	store.HeartbeatPost: http.MethodPost,
	store.HeartbeatHead: http.MethodHead,
}

func (h *Heartbeat) success() {
	now := h.clk.Now()
	h.mu.Lock()
	h.status.LastSuccessAt = now
	h.mu.Unlock()
	h.loggedFailure = ""
}

// failure 记下这次失败的时刻与类别，并只在状态变化时记一行日志（连续同一失败不刷屏）。err 只用于日志，经
// outbound.WithoutURL 去掉 URL：目标地址可能就是凭据。
func (h *Heartbeat) failure(category string, status int, err error) {
	now := h.clk.Now()
	h.mu.Lock()
	h.status.LastFailureAt = now
	h.status.FailureCategory = category
	h.status.FailureHTTPStatus = status
	h.mu.Unlock()
	sig := category
	if status != 0 {
		sig = fmt.Sprintf("%s:%d", category, status)
	}
	if sig == h.loggedFailure {
		return
	}
	h.loggedFailure = sig
	if err != nil {
		h.log.Warn("heartbeat failed", "category", category, "err", outbound.WithoutURL(err))
		return
	}
	h.log.Warn("heartbeat failed", "category", category, "status", status)
}

func waitReal(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
