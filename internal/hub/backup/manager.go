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
	ListObjectsV2(context.Context, string, int) ([]s3.Object, error)
	DeleteObject(context.Context, string) error
}

type LayerStatus struct {
	LastSuccess time.Time
	Failure     string
	Since       time.Time
	StatusCode  int
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
	logged      bool
	lastLog     time.Duration
}

type Manager struct {
	st          *store.Store
	sender      Sender
	clk         clock.Clock
	log         *slog.Logger
	newClient   func(s3.Config) (objectStore, error)
	runMu       [2]sync.Mutex
	initMu      sync.Mutex
	initialized bool
	mu          sync.RWMutex
	layers      [2]layerState
}

func New(st *store.Store, sender Sender, clk clock.Clock, log *slog.Logger) *Manager {
	return &Manager{st: st, sender: sender, clk: clk, log: log,
		newClient: func(cfg s3.Config) (objectStore, error) { return s3.New(cfg, clk) }}
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	cfg, err := m.st.BackupSettings(ctx)
	// 设置不可读时仍展示已观察的故障；状态读取本身不伪造首次失败时刻。
	settingsErr := err
	times, err := m.st.BackupSuccessTimes(ctx)
	if err != nil {
		return Status{}, err
	}
	m.mu.RLock()
	out := Status{Enabled: cfg.Target.Enabled(), Config: m.layers[0].LayerStatus, Metrics: m.layers[1].LayerStatus}
	m.mu.RUnlock()
	if out.Config.LastSuccess.IsZero() {
		out.Config.LastSuccess = times[store.MaintenanceBackupConfig]
	}
	if out.Metrics.LastSuccess.IsZero() {
		out.Metrics.LastSuccess = times[store.MaintenanceBackupMetrics]
	}
	if out.Config.Failure == "" {
		since, err := m.st.BackupFailingSince(ctx)
		if err != nil {
			return Status{}, err
		}
		if !since.IsZero() {
			out.Config.Failure, out.Config.Since = "unrecovered", since
		}
	}
	if settingsErr != nil && out.Config.Failure == "" {
		return Status{}, settingsErr
	}
	return out, nil
}

const (
	retentionBudget    = 5 * time.Minute
	maxListedObjects   = 10000
	failureLogInterval = 5 * time.Minute
	configUploadBase   = 5 * time.Minute
	bytesPerMiB        = 1 << 20
)

// 两层各自循环、各自互斥，每秒重新读设置；指标层在途不阻塞配置层。
// 首轮由持久化成功时刻折算等待，之后用单调钟计周期，慢上传不积累待执行次数。
// 墙钟用于快照、事件和成功时刻；对象名的墙钟顺序也是保留顺序，但刚上传的对象不参与删除。
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range m.layers {
		wg.Add(1)
		go func() { defer wg.Done(); m.runLayer(ctx, i) }()
	}
	wg.Wait()
}

func (m *Manager) runLayer(ctx context.Context, i int) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var logged bool
	var lastLog time.Duration
	var wake <-chan struct{}
	if i == 0 {
		wake = m.st.ThemeChanges()
	}
	force := false
	for {
		if err := m.tickLayerWithWake(ctx, i, force); err != nil && ctx.Err() == nil && (!logged || m.clk.Mono()-lastLog >= failureLogInterval) {
			m.log.Error("backup scheduling failed", "layer", i, "err", err)
			logged, lastLog = true, m.clk.Mono()
		}
		force = false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
			force = true
		}
	}
}

func (m *Manager) Tick(ctx context.Context) error {
	var result error
	for i := range m.layers {
		result = errors.Join(result, m.tickLayer(ctx, i))
	}
	return result
}

func (m *Manager) initialize(ctx context.Context) error {
	m.initMu.Lock()
	defer m.initMu.Unlock()
	if m.initialized {
		return nil
	}
	times, err := m.st.BackupSuccessTimes(ctx)
	if err != nil {
		return err
	}
	since, err := m.st.BackupFailingSince(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(m.st.BackupDirectory())
	if err != nil {
		return err
	}
	// serve 装配并运行一个 Manager；initMu 让清理先于该 Manager 两层创建目录完成。
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "probe-backup-") {
			if err := os.RemoveAll(filepath.Join(m.st.BackupDirectory(), entry.Name())); err != nil {
				return err
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, name := range []string{store.MaintenanceBackupConfig, store.MaintenanceBackupMetrics} {
		state := &m.layers[i]
		state.LastSuccess = times[name]
		if !state.LastSuccess.IsZero() {
			// 未来成功时刻最多等待一个周期；过旧时刻在 tickLayer 中立即到期。
			elapsed := max(time.Duration(0), m.clk.Now().Sub(state.LastSuccess))
			state.attempted, state.lastAttempt = true, m.clk.Mono()-elapsed
		}
	}
	if !since.IsZero() {
		m.layers[0].notified = true
		m.layers[0].Failure, m.layers[0].Since = "unrecovered", since
	}
	m.initialized = true
	return nil
}

func (m *Manager) tickLayer(ctx context.Context, i int) error {
	return m.tickLayerWithWake(ctx, i, false)
}

func (m *Manager) tickLayerWithWake(ctx context.Context, i int, force bool) error {
	m.runMu[i].Lock()
	defer m.runMu[i].Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.initialize(ctx); err != nil {
		return err
	}
	if i == 0 {
		select {
		case <-m.st.ThemeChanges():
			force = true
		default:
		}
	}
	cfg, err := m.st.BackupSettings(ctx)
	if err != nil {
		if i == 0 {
			return m.finish(ctx, i, "config", "settings", 0, err.Error())
		}
		return nil
	}
	state := &m.layers[i]
	if !cfg.Target.Enabled() {
		state.attempted = false
		return nil
	}
	layer := "config"
	interval, keep := cfg.ConfigIntervalS, cfg.ConfigKeep
	if i == 1 {
		layer = "metrics"
		interval, keep = cfg.MetricsIntervalS, cfg.MetricsKeep
	}
	if !force && state.attempted && m.clk.Mono()-state.lastAttempt < time.Duration(interval)*time.Second {
		return nil
	}
	category, code, detail := m.perform(ctx, cfg, layer, keep)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	state.attempted, state.lastAttempt = true, m.clk.Mono()
	return m.finish(ctx, i, layer, category, code, detail)
}

func (m *Manager) perform(ctx context.Context, cfg store.BackupSettings, layer string, keep uint32) (string, int, string) {
	// 每轮刚上传的对象至少占一份；份数 0 无法满足保留不变式，必须在上传和删除前拒绝。
	if keep == 0 {
		return "retention_config", 0, ""
	}
	client, err := m.newClient(cfg.Target)
	if err != nil {
		return "client", 0, err.Error()
	}
	dir, err := os.MkdirTemp(m.st.BackupDirectory(), "probe-backup-")
	if err != nil {
		return "snapshot", 0, err.Error()
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
		return "snapshot", 0, err.Error()
	}
	f, err := os.Open(path)
	if err != nil {
		return "snapshot", 0, err.Error()
	}
	prefix := strings.TrimRight(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += layer + "/"
	key := prefix + m.clk.Now().UTC().Format("20060102T150405.000000000Z") + ".db"
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return "snapshot", 0, err.Error()
	}
	// 复用出站边界而非客户端总时限；每次请求由自己的 ctx 截止时间约束。
	uploadCtx, cancelUpload := context.WithTimeout(ctx, uploadBudget(cfg, layer, info.Size()))
	err = client.PutObject(uploadCtx, key, f)
	cancelUpload()
	closeErr := f.Close()
	if err != nil {
		return failure("upload", err)
	}
	if closeErr != nil {
		return "snapshot", 0, closeErr.Error()
	}
	if err := os.Remove(path); err != nil {
		return "cleanup", 0, err.Error()
	}
	// 列举与整轮删除共用五分钟预算；对象总量上限包含历史清理失败积累的额外快照。
	// 上限限制翻页与内存，并暴露清理积压；按最旧删除时截断列表只会少删，不会错删应保留对象。
	retentionCtx, cancelRetention := context.WithTimeout(ctx, retentionBudget)
	defer cancelRetention()
	objects, err := client.ListObjectsV2(retentionCtx, prefix, maxListedObjects)
	if err != nil {
		return failure("list", err)
	}
	// 仅删除本层生成的对象；同 bucket 里的主题、其他前缀及非备份文件不属于保留策略。
	objects = slices.DeleteFunc(objects, func(o s3.Object) bool {
		if o.Key == key {
			return true
		}
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
	// 本轮已上传对象占一份，即使列举暂未看到它也不再删除它。
	for excess := len(objects) - (int(keep) - 1); excess > 0; excess-- {
		if err := client.DeleteObject(retentionCtx, objects[0].Key); err != nil {
			return failure("delete", err)
		}
		objects = objects[1:]
	}
	if err := os.Remove(dir); err != nil {
		return "cleanup", 0, err.Error()
	}
	// 唤醒和周期都执行完整配置轮；主题成功不能掩盖快照或保留的故障，整轮成功后才记账和恢复通知。
	if layer == "config" {
		if category, code, detail := m.syncThemes(ctx, cfg, client); category != "" {
			return category, code, detail
		}
	}
	if err := m.st.RecordBackupSuccess(ctx, layer); err != nil {
		return "record", 0, err.Error()
	}
	return "", 0, ""
}

func uploadBudget(cfg store.BackupSettings, layer string, size int64) time.Duration {
	if layer == "metrics" {
		// 指标上传占自己的一个周期减去保留预算，不挤占配置层。默认 86400-300=86100 秒，
		// 2 GiB 要求有效载荷平均至少 2048/86100=0.02379 MiB/s（约 0.200 Mbit/s），线路还需额外开销。
		// 任意大小 S 的最低速率为 S/预算；低于该速率会超时失败，指标层按设计不告警。
		return time.Duration(cfg.MetricsIntervalS)*time.Second - retentionBudget
	}
	return configUploadBase + time.Duration((size+bytesPerMiB-1)/bytesPerMiB)*time.Second
}

func failure(stage string, err error) (string, int, string) {
	var remote *s3.Error
	if errors.As(err, &remote) {
		return stage + "/" + remote.Kind, remote.StatusCode, remote.Detail
	}
	return stage, 0, err.Error()
}

func (m *Manager) finish(ctx context.Context, i int, layer, category string, code int, detail string) error {
	m.mu.Lock()
	state := &m.layers[i]
	previous := state.Failure
	if category != "" {
		if previous == "" {
			state.Since = m.clk.Now()
		}
		state.Failure = category
		state.StatusCode = code
	} else {
		state.LastSuccess = time.Unix(m.clk.Now().Unix(), 0).UTC()
	}
	notified := state.notified
	since := state.Since
	logFailure := category != "" && (!state.logged || previous != category || m.clk.Mono()-state.lastLog >= failureLogInterval)
	if logFailure {
		state.logged, state.lastLog = true, m.clk.Mono()
	}
	m.mu.Unlock()
	if logFailure {
		m.log.Warn("backup failed", "layer", layer, "category", category, "status_code", code, "detail", detail)
	}
	// 配置层的未恢复标记与事件由 RecordBackupEvent 同事务提交，initialize 在首次执行前读回。
	// 因而持续故障跨重启也不重复通知；事务失败保留原通知状态，下次重试。
	// 指标缺口由后续上报继续产生新数据，不触发通知，故障仍由日志与同一状态接口可见。
	if i == 0 && (category != "" && !notified || category == "" && notified) {
		transition, summary := store.TransitionFiring, fmt.Sprintf("config 层备份失败（%s）", category)
		if category == "" {
			transition, summary = store.TransitionRecovered, "config 层备份已恢复"
		}
		ev, err := m.st.RecordBackupEvent(ctx, transition, summary, since)
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
		state.StatusCode, state.logged = 0, false
		m.mu.Unlock()
	}
	return nil
}
