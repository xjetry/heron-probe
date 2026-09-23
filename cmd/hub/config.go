package main

import (
	"fmt"
	"net"
	"time"

	"github.com/xjetry/probe/internal/hub/ingest"
)

const (
	defaultTTL = 30 * time.Second
	minTTL     = ingest.MinTTL
)

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

// loadZone 解析 --timezone：空串取本机时区。周期边界是"重置日零点"，只有带时区才有定义；
// 出错信息带上标志名，启动失败时运维不必猜是哪个参数。
func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("--timezone: %w", err)
	}
	return loc, nil
}
