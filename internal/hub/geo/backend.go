package geo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/xjetry/probe/internal/hub/store"
)

// Backend 只负责一次查询；准入、校验、退避与条件写入由 Resolver 承载。
type Backend interface {
	// Lookup 用 Resolver 判定准入时读到的那份设置查询：HTTP 用 s.URL 组目标，mmdb 忽略 s。
	Lookup(ctx context.Context, s store.GeoSettings, addr netip.Addr) (string, error)
	// Service 给出退避键里"服务"一项：HTTP 为 s.URL（换服务地址即换键），mmdb 为库路径（改 geo.url 不影响 mmdb 的退避）。
	Service(s store.GeoSettings) string
}

var errNotCountry = errors.New("response is not two uppercase letters")

// maxResponseBytes 是读取应答体的上限。合法应答是两个字母加少量空白，超出即判失败，不再往下读。
const maxResponseBytes = 64

// HTTP 把地址发给设置里的服务地址。它不持有 store：发往哪里只由调用方传入的那份设置决定，而 Resolver 传入的正是它
// 判定开关时读到的那一份，所以同一次外呼的开关与服务地址出自同一次读取（见 Resolver.Sweep 的不变式）。
type HTTP struct {
	client *http.Client
}

// NewHTTP 的 client 应当是通知渠道用的那一个（alert.NewHTTPClient：不跟随重定向、带总超时），hub 的出站行为只有一套。
func NewHTTP(client *http.Client) *HTTP {
	return &HTTP{client: client}
}

// Lookup 发出一次查询。请求只带地址：GET、无请求体，不设任何凭据头；服务地址不含用户信息由 api 的 UpdateSettings
// 保证。只认 200：客户端不跟随重定向，3xx 在这里按失败处理。
func (h *HTTP) Lookup(ctx context.Context, s store.GeoSettings, addr netip.Addr) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Target(s.URL, addr), nil)
	if err != nil {
		return "", err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxResponseBytes {
		return "", fmt.Errorf("response longer than %d bytes", maxResponseBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

// Service 是服务地址：运维换了服务，旧服务留下的退避不挡住对新服务的查询。
func (h *HTTP) Service(s store.GeoSettings) string { return s.URL }

type MMDB struct {
	path string
	db   *maxminddb.Reader
}

func OpenMMDB(path string) (*MMDB, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--geo-mmdb %q: %w", path, err)
	}
	return &MMDB{path: path, db: db}, nil
}

// Lookup 只读 GeoLite2-Country 的 country.iso_code，不以 registered_country 等字段替代。
// 查不到或缺字段得到空串，由 Resolver 的 store.IsCountryCode 校验按失败退避，不沿用其它含义的国家。
func (m *MMDB) Lookup(ctx context.Context, _ store.GeoSettings, addr netip.Addr) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := m.db.Lookup(addr).Decode(&record); err != nil {
		return "", err
	}
	return record.Country.ISOCode, nil
}

// Service 是库路径，不取 geo.url：mmdb 下 geo.url 不生效，改它不应清掉查不到的地址的退避。
func (m *MMDB) Service(store.GeoSettings) string { return m.path }

// Close 必须在 Resolver 的查询循环退出后调用，读取与关闭不能并发。
func (m *MMDB) Close() error { return m.db.Close() }
