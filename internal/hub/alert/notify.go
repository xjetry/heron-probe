package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

type Channel interface {
	Send(context.Context, Message) error
}
type Retryable interface{ Retryable() bool }

// sendFailure 是渠道每条失败路径的唯一错误形状：类别在产生失败的地方确定，队列据此落库，
// 不从错误文本反推。detail 是落库的原文，不含 URL（outboundError 负责剥离）。
type sendFailure struct {
	failure store.DeliveryFailure
	status  int // 仅 FailureHTTPStatus 非零。
	detail  string
	err     error // 底层原因，保留 errors.Is(err, ErrInvalid) 等判定；HTTP 应答失败没有。
}

func failure(kind store.DeliveryFailure, err error) error {
	return &sendFailure{failure: kind, detail: err.Error(), err: err}
}

// Error 是 TestNotifyChannel 原样给面板的文本，形状沿用 "HTTP 401 Unauthorized: <片段>"、"Post: <原因>"。
func (e *sendFailure) Error() string {
	if e.failure == store.FailureHTTPStatus {
		return fmt.Sprintf("HTTP %d %s: %s", e.status, http.StatusText(e.status), e.detail)
	}
	return e.detail
}
func (e *sendFailure) Unwrap() error { return e.err }

// 可重试只由类别与状态码决定：没收到应答与 HTTP 5xx、408、429 可重试；
// 其余失败（其他非 2xx、请求无法构造、渠道配置无效）原样重发只会得到同样结果。
func (e *sendFailure) Retryable() bool {
	switch e.failure {
	case store.FailureTransport:
		return true
	case store.FailureHTTPStatus:
		return e.status >= 500 || e.status == 408 || e.status == 429
	}
	return false
}

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// URL 可含渠道凭据；构造请求与传输失败共用此出口，只保留操作及底层原因。
func outboundError(kind store.DeliveryFailure, err error) error {
	var target *url.Error
	if errors.As(err, &target) {
		err = fmt.Errorf("%s: %v", target.Op, target.Err)
	}
	return failure(kind, err)
}

// range 只用来定位字符边界，不会改写非法字节；截断后清洗保证落库及协议 string 合法。
func responseSummary(data []byte) string {
	text := string(data)
	n := 0
	for i := range text {
		if n == 200 {
			text = text[:i]
			break
		}
		n++
	}
	return strings.ToValidUTF8(text, "�")
}

// 两种渠道共用出站边界：不信任响应体长度，错误诊断也只保留有限前缀。
func sendHTTP(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return outboundError(store.FailureRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return outboundError(store.FailureTransport, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &sendFailure{failure: store.FailureHTTPStatus, status: resp.StatusCode, detail: responseSummary(data)}
	}
	// 接收端已用 2xx 确认送达；读体中断不否定确认，重试只会重复通知。
	return nil
}

type telegram struct {
	cfg    TelegramConfig
	client *http.Client
	base   string
}

func NewTelegram(cfg TelegramConfig, client *http.Client, base string) Channel {
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &telegram{cfg, client, strings.TrimRight(base, "/")}
}
func (t *telegram) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]string{"chat_id": t.cfg.ChatID, "text": m.Summary})
	if err != nil {
		return failure(store.FailureRequest, err)
	}
	return sendHTTP(ctx, t.client, "POST", t.base+"/bot"+t.cfg.BotToken+"/sendMessage", nil, body)
}

type webhook struct {
	cfg      WebhookConfig
	client   *http.Client
	template *template.Template
}

func NewWebhook(cfg WebhookConfig, client *http.Client) (Channel, error) {
	tmpl, err := checkWebhook(cfg)
	if err != nil {
		return nil, failure(store.FailureChannelInvalid, err)
	}
	return webhookFrom(cfg, client, tmpl)
}
func webhookFrom(cfg WebhookConfig, client *http.Client, tmpl *template.Template) (Channel, error) {
	// SaveChannel 在写侧规范 method；不能让未规范的配置被 net/http 默认为 GET。
	if cfg.Method == "" {
		return nil, failure(store.FailureChannelInvalid, invalid("webhook.method", "must be normalized to POST, PUT or PATCH before delivery"))
	}
	cfg.Headers = maps.Clone(cfg.Headers)
	return &webhook{cfg, client, tmpl}, nil
}
func (w *webhook) Send(ctx context.Context, m Message) error {
	var body bytes.Buffer
	if err := w.template.Execute(&body, m); err != nil {
		return failure(store.FailureRequest, err)
	}
	return sendHTTP(ctx, w.client, w.cfg.Method, w.cfg.URL, w.cfg.Headers, body.Bytes())
}

func ParseChannel(c store.NotifyChannel, client *http.Client, telegramBase string) (Channel, error) {
	parsed, err := parseChannelConfig(c)
	if err != nil {
		return nil, failure(store.FailureChannelInvalid, err)
	}
	if c.Kind == store.ChannelTelegram {
		return NewTelegram(parsed.telegram, client, telegramBase), nil
	}
	return webhookFrom(parsed.webhook, client, parsed.template)
}
