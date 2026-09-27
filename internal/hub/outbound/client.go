package outbound

import (
	"net/http"
	"time"
)

// NewClient 供 hub 主动出站复用；重定向不得把长期凭据带到配置之外的目标。
// 响应体上限由各协议读侧承载，下载对象与通知诊断不是同一种体量。
func NewClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
