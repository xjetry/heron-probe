// Package backup 负责分层快照、上传、按份保留及配置层故障通知。
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/s3"
	"github.com/xjetry/probe/internal/hub/store"
)

type Sender interface{ Enqueue(store.AlertEvent) }

type objectStore interface {
	PutObject(context.Context, string, io.ReadSeeker) error
	ListObjectsV2(context.Context, string) ([]s3.Object, error)
	DeleteObject(context.Context, string) error
}

type LayerStatus struct {
	LastSuccess time.Time
	Failure     string
	Since       time.Time
}

type Status struct {
	Enabled         bool
	Config, Metrics LayerStatus
}

type layerState struct {
	LayerStatus
	attempted   bool
	lastAttempt time.Duration
	notified    bool
}

type Manager struct {
	st        *store.Store
	sender    Sender
	clk       clock.Clock
	log       *slog.Logger
	newClient func(s3.Config) (objectStore, error)
	runMu     sync.Mutex
	mu        sync.RWMutex
	layers    [2]layerState
}

func New(st *store.Store, sender Sender, clk clock.Clock, log *slog.Logger) *Manager {
	return &Manager{st: st, sender: sender, clk: clk, log: log,
		newClient: func(cfg s3.Config) (objectStore, error) { return s3.New(cfg) }}
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	cfg, err := m.st.BackupSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	times, err := m.st.BackupSuccessTimes(ctx)
	if err != nil {
		return Status{}, err
	}
	m.mu.RLock()
	out := Status{Enabled: cfg.Target.Enabled(), Config: m.layers[0].LayerStatus, Metrics: m.layers[1].LayerStatus}
	m.mu.RUnlock()
	if out.Config.LastSuccess.IsZero() {
		out.Config.LastSuccess = times["backup_config"]
	}
	if out.Metrics.LastSuccess.IsZero() {
		out.Metrics.LastSuccess = times["backup_metrics"]
	}
	return out, nil
}

// 一个循环串行执行两层；每秒重新读设置，启停与改周期不需要重启。
// 周期用单调钟，墙钟只用于对象名及面板。一次慢上传不会积累待执行的周期。
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := m.Tick(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("backup scheduling failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) Tick(ctx context.Context) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	cfg, err := m.st.BackupSettings(ctx)
	if err != nil {
		return err
	}
	if !cfg.Target.Enabled() {
		for i := range m.layers {
			m.layers[i].attempted = false
		}
		return nil
	}
	var result error
	for i, layer := range []string{"config", "metrics"} {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		interval, keep := cfg.ConfigIntervalS, cfg.ConfigKeep
		if i == 1 {
			interval, keep = cfg.MetricsIntervalS, cfg.MetricsKeep
		}
		state := &m.layers[i]
		if state.attempted && m.clk.Mono()-state.lastAttempt < time.Duration(interval)*time.Second {
			continue
		}
		category := m.perform(ctx, cfg, layer, keep)
		if ctx.Err() != nil {
			return errors.Join(result, ctx.Err())
		}
		state.attempted, state.lastAttempt = true, m.clk.Mono()
		if err := m.finish(ctx, i, layer, category); err != nil {
			result = errors.Join(result, fmt.Errorf("%s backup event: %w", layer, err))
		}
	}
	return result
}

func (m *Manager) perform(ctx context.Context, cfg store.BackupSettings, layer string, keep uint32) string {
	// 份数 0 会删掉这一层全部对象，必须在任何上传和删除前拒绝。
	if keep == 0 {
		return "retention_config"
	}
	client, err := m.newClient(cfg.Target)
	if err != nil {
		return "client"
	}
	dir, err := os.MkdirTemp("", "probe-backup-")
	if err != nil {
		return "snapshot"
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			m.log.Error("backup cleanup failed", "layer", layer, "err", err)
		}
	}()
	path := filepath.Join(dir, "snapshot.db")
	snapshot := m.st.SnapshotConfig
	if layer == "metrics" {
		snapshot = m.st.SnapshotMetrics
	}
	if err := snapshot(ctx, path); err != nil {
		m.log.Error("backup snapshot failed", "layer", layer, "err", err)
		return "snapshot"
	}
	f, err := os.Open(path)
	if err != nil {
		return "snapshot"
	}
	prefix := strings.TrimRight(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += layer + "/"
	key := prefix + m.clk.Now().UTC().Format("20060102T150405.000000000Z") + ".db"
	err = client.PutObject(ctx, key, f)
	closeErr := f.Close()
	if err != nil {
		return failure("upload", err)
	}
	if closeErr != nil {
		return "snapshot"
	}
	if err := os.Remove(path); err != nil {
		return "cleanup"
	}
	objects, err := client.ListObjectsV2(ctx, prefix)
	if err != nil {
		return failure("list", err)
	}
	// 仅删除本层生成的对象；同 bucket 里的主题、其他前缀及非备份文件不属于保留策略。
	objects = slices.DeleteFunc(objects, func(o s3.Object) bool {
		if !strings.HasPrefix(o.Key, prefix) {
			return true
		}
		name := strings.TrimPrefix(o.Key, prefix)
		if !strings.HasSuffix(name, ".db") {
			return true
		}
		_, err := time.Parse("20060102T150405.000000000Z", strings.TrimSuffix(name, ".db"))
		return err != nil
	})
	slices.SortFunc(objects, func(a, b s3.Object) int { return strings.Compare(a.Key, b.Key) })
	for excess := len(objects) - int(keep); excess > 0; excess-- {
		if err := client.DeleteObject(ctx, objects[0].Key); err != nil {
			return failure("delete", err)
		}
		objects = objects[1:]
	}
	if err := os.Remove(dir); err != nil {
		return "cleanup"
	}
	if err := m.st.RecordBackupSuccess(ctx, layer); err != nil {
		return "record"
	}
	return ""
}

func failure(stage string, err error) string {
	var remote *s3.Error
	if errors.As(err, &remote) {
		return stage + "/" + remote.Kind
	}
	return stage
}

func (m *Manager) finish(ctx context.Context, i int, layer, category string) error {
	m.mu.Lock()
	state := &m.layers[i]
	previous := state.Failure
	if category != "" {
		if previous == "" {
			state.Since = m.clk.Now()
		}
		state.Failure = category
	} else {
		state.LastSuccess = time.Unix(m.clk.Now().Unix(), 0).UTC()
	}
	notified := state.notified
	m.mu.Unlock()
	if category != "" {
		m.log.Warn("backup failed", "layer", layer, "category", category)
	}
	// 配置与凭据不可自愈，只在故障首次落事件，持续失败不重复发；写事件失败则保留未通知状态供重试。
	// 指标缺口由后续上报继续产生新数据，不触发通知，故障仍由日志与同一状态接口可见。
	if i == 0 && (category != "" && !notified || category == "" && notified) {
		transition, summary := store.TransitionFiring, fmt.Sprintf("config 层备份失败（%s）", category)
		if category == "" {
			transition, summary = store.TransitionRecovered, "config 层备份已恢复"
		}
		ev, err := m.st.RecordBackupEvent(ctx, transition, summary)
		if err != nil {
			return err
		}
		if m.sender != nil {
			m.sender.Enqueue(ev)
		}
		m.mu.Lock()
		state.notified = category != ""
		m.mu.Unlock()
	}
	if category == "" {
		m.mu.Lock()
		state.Failure, state.Since = "", time.Time{}
		m.mu.Unlock()
	}
	return nil
}
