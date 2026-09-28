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

// Backend 只负责地址到国家码的查询；准入、校验、退避与条件写入由 Resolver 统一承载。
type Backend interface {
	Lookup(context.Context, netip.Addr) (string, error)
}

var errNotCountry = errors.New("response is not two uppercase letters")

type HTTP struct {
	store  *store.Store
	client *http.Client
}

// NewHTTP 的 client 与通知渠道共用（alert.NewHTTPClient），不跟随重定向且带总超时。
func NewHTTP(st *store.Store, client *http.Client) *HTTP {
	return &HTTP{store: st, client: client}
}

// Lookup 读取运行配置中的目标，只带地址、不带凭据。URL 的用户信息由 UpdateSettings 拒绝。
func (h *HTTP) Lookup(ctx context.Context, addr netip.Addr) (string, error) {
	settings, err := h.store.GeoSettings(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Target(settings.URL, addr), nil)
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
	const maxResponseBytes = 64
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxResponseBytes {
		return "", fmt.Errorf("response longer than %d bytes", maxResponseBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

type MMDB struct {
	db *maxminddb.Reader
}

func OpenMMDB(path string) (*MMDB, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--geo-mmdb %q: %w", path, err)
	}
	return &MMDB{db: db}, nil
}

// Lookup 只读 GeoLite2-Country 的 country.iso_code，不以 registered_country 等字段替代。
// 查不到或缺字段得到空串，由 Resolver 的 store.IsCountryCode 校验按失败退避，不沿用其它含义的国家。
func (m *MMDB) Lookup(ctx context.Context, addr netip.Addr) (string, error) {
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

// Close 必须在 Resolver 的查询循环退出后调用，读取与关闭不能并发。
func (m *MMDB) Close() error { return m.db.Close() }
