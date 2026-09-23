package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/probelimit"
)

const (
	defaultTTL = 30 * time.Second
	minTTL     = ingest.MinTTL
	maxTTL     = 180 * time.Second
)

// 间隔为 TTL/3，一次满速产出至多 MaxTasksPerNode×(TTL/3)/MinIntervalS 条；批次须容纳它并留余量。
const _ = uint(probelimit.MaxResultsPerReport*probelimit.MinIntervalS*3 - probelimit.MaxTasksPerNode*int(maxTTL/time.Second))

// parseTTL 解析 PROBE_OFFLINE_AFTER。TTL 是离线发现延迟的上界，也是这条链上
// 唯一被直接配置的量：上报间隔、退避上限、告警宽限期下限都由它反推。
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return defaultTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %w", err)
	}
	if d < minTTL {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %v is below the minimum %v", d, minTTL)
	}
	if d > maxTTL {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %v is above the maximum %v", d, maxTTL)
	}
	return d, nil
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var localtimePath = "/etc/localtime"

// loadZone 的三个来源都经 namedZone 规范化后加载，Location 的名字会随流量响应交给面板。
// fallback 只表示本机时区不可判定，serve 据此告警；显式指定的无效时区仍拒绝启动。
func loadZone(name string) (*time.Location, bool, error) {
	if name != "" {
		loc, err := namedZone(name)
		return loc, false, err
	}
	if name := os.Getenv("TZ"); name != "" {
		if loc, err := namedZone(name); err == nil {
			return loc, false, nil
		}
	}
	if target, err := filepath.EvalSymlinks(localtimePath); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok && name != "" {
			if loc, err := namedZone(name); err == nil {
				return loc, false, nil
			}
		}
	}
	return time.UTC, true, nil
}

// canonicalZone 去掉 tzdata 的编码目录前缀；同一区域的周期判定与面板 Intl 格式化必须共用规范名。
// Go 能加载的名字不一定能被 Intl 接受，Local 和空串也没有可下发的区域含义。
func canonicalZone(name string) (string, bool) {
	name = strings.TrimPrefix(name, "posix/")
	name = strings.TrimPrefix(name, "right/")
	return name, name != "" && name != "Local"
}

func namedZone(name string) (*time.Location, error) {
	canonical, ok := canonicalZone(name)
	if !ok {
		return nil, fmt.Errorf("--timezone: %q is not an IANA time zone", name)
	}
	loc, err := time.LoadLocation(canonical)
	if err != nil {
		return nil, fmt.Errorf("--timezone: %w", err)
	}
	return loc, nil
}
