package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// downloadTimeout 是一次取回的总时限，GitHub 与 hub 两种来源相同。
const downloadTimeout = 5 * time.Minute

type source interface {
	Fetch(context.Context, Request, string, string) (Artifacts, error)
}

// sourceChoice 是更新器启动时按本机安装参数选定的取产物来源（spec §4.10）。
type sourceChoice struct {
	// name 是 "github" 或 "hub"，随状态上报（Status.Source）。
	name string
	src  source
	// err 非空表示来源配置读不出：更新器照常运行、回答状态，但不支持更新，原因写进状态。
	// 不让进程退出——崩溃循环在面板上只表现为"没有上报更新能力"，看不出原因。
	err string
}

// machine 只由本机安装参数构造，请求不能选择路径、命令或服务单元。
type machine interface {
	Current(context.Context) (string, error)
	Stage([]byte) (string, error)
	Stop(context.Context) error
	Backup() error
	Install() error
	Start(context.Context) error
	Verify(context.Context, int, string) error
	Restore() error
	Gate(bool) error
}

type Engine struct {
	mu               sync.Mutex
	job              *Job
	history          []Request
	restart          bool
	path, role, arch string
	choice           sourceChoice
	keys             []ed25519.PublicKey
	machine          machine
	version          string
	reason           string
	recoveryError    string
	readyTimeout     time.Duration
	// working 覆盖终态落盘到 worker 退出的窗口，避免新任务抢用同一份备份。
	working     bool
	maintenance bool
	ctx         context.Context
}

type journal struct {
	Job     *Job      `json:"job"`
	History []Request `json:"history"`
	Restart bool      `json:"restart"`
}

func newEngine(ctx context.Context, path, role, arch string, choice sourceChoice, keys []ed25519.PublicKey, m machine) (*Engine, error) {
	e := &Engine{ctx: ctx, path: path, role: role, arch: arch, choice: choice, keys: keys, machine: m, readyTimeout: 90 * time.Second}
	data, err := os.ReadFile(path)
	if err == nil {
		var stored journal
		if err = json.Unmarshal(data, &stored); err != nil {
			return nil, err
		}
		if stored.Job == nil {
			return nil, errors.New("updater journal has no transaction")
		}
		j := *stored.Job
		e.history = stored.History
		e.restart = stored.Restart
		e.job = &j
		if j.Active() {
			if err = e.recover(); err != nil {
				return nil, err
			}
		} else if err = m.Gate(false); err != nil {
			return nil, err
		}
		if e.restart {
			if err = m.Start(ctx); err != nil {
				return nil, err
			}
			e.restart = false
			if err = e.save(*e.job); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	e.version, err = m.Current(ctx)
	if err != nil {
		e.reason = boundedError(err.Error())
	}
	return e, nil
}

func (e *Engine) status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.working {
		ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
		v, err := e.machine.Current(ctx)
		cancel()
		if err != nil {
			e.reason = boundedError(err.Error())
		} else {
			e.version = v
			e.reason = ""
		}
	}
	s := Status{Protocol: Protocol, Supported: e.reason == "" && ValidVersion(e.version), Version: e.version, Reason: e.reason, Source: e.choice.name}
	if e.recoveryError != "" {
		s.Supported, s.Reason = false, e.recoveryError
	}
	if e.choice.err != "" && e.recoveryError == "" {
		s.Supported, s.Reason = false, e.choice.err
	}
	if !s.Supported && s.Reason == "" {
		s.Reason = "installed version is not an official version"
	}
	if e.job != nil {
		j := *e.job
		s.Job = &j
	}
	return s
}

func (e *Engine) save(j Job) error {
	j.UpdatedAt = time.Now().Unix()
	history := make([]Request, 0, len(e.history)+1)
	seen := false
	for _, r := range e.history {
		if r.ExpiresAt > time.Now().Unix() {
			history = append(history, r)
			seen = seen || r.ID == j.ID
		}
	}
	if !seen {
		history = append(history, j.Request)
	}
	b, err := json.Marshal(journal{Job: &j, History: history, Restart: e.restart})
	if err != nil {
		return err
	}
	if err = atomicWrite(e.path, b, 0600); err != nil {
		return err
	}
	e.job = &j
	e.history = history
	return nil
}

func (e *Engine) submit(r Request) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.maintenance {
		return Job{}, errors.New("updater is in installer maintenance mode")
	}
	if e.recoveryError != "" {
		return Job{}, errors.New(e.recoveryError)
	}
	if e.choice.err != "" {
		return Job{}, errors.New(e.choice.err)
	}
	if e.job != nil && e.job.ID == r.ID {
		if e.job.Request != r {
			return Job{}, errors.New("task ID already has different parameters")
		}
		return *e.job, nil
	}
	if err := r.Validate(time.Now()); err != nil {
		return Job{}, err
	}
	activeHistory := 0
	for _, old := range e.history {
		if old.ID == r.ID {
			return Job{}, errors.New("task ID was already consumed")
		}
		if old.ExpiresAt > time.Now().Unix() {
			activeHistory++
		}
	}
	if activeHistory >= 4096 {
		return Job{}, errors.New("too many update attempts; wait for old requests to expire")
	}
	if e.working || e.job != nil && e.job.Active() {
		return Job{}, errors.New("an update is already running")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	current, err := e.machine.Current(ctx)
	if err != nil {
		return Job{}, err
	}
	e.version = current
	e.reason = ""
	if !Newer(r.Version, e.version) {
		return Job{}, errors.New("target must be newer than the installed official version")
	}
	j := Job{Request: r, State: "queued"}
	if err := e.save(j); err != nil {
		return Job{}, err
	}
	e.working = true
	go e.run()
	return *e.job, nil
}

func (e *Engine) enterMaintenance() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recoveryError != "" {
		return errors.New(e.recoveryError)
	}
	if e.maintenance {
		return errors.New("another installer already reserved the updater")
	}
	if e.working || e.job != nil && e.job.Active() {
		return errors.New("an update is active; wait for completion before reinstalling")
	}
	e.maintenance = true
	return nil
}

func (e *Engine) transition(state string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := *e.job
	j.State = state
	return e.save(j)
}

func (e *Engine) run() {
	defer func() { e.mu.Lock(); e.working = false; e.mu.Unlock() }()
	if err := e.execute(); err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		// 成功已经持久化后不能再回滚：hub 可能已经开始接受业务写入。
		if e.job.State == "succeeded" {
			return
		}
		cause := err.Error()
		if recoveryErr := e.recover(); recoveryErr != nil {
			cause += "; recovery failed: " + recoveryErr.Error()
			// 恢复未完成时必须保留事务与备份；重启更新器完成恢复后才允许新的写操作。
			e.recoveryError = boundedError(cause)
		}
		j := *e.job
		j.Error = boundedError(cause)
		_ = e.save(j)
	}
}

func (e *Engine) execute() error {
	if err := e.transition("downloading"); err != nil {
		return err
	}
	j := e.status().Job
	downloadCtx, cancelDownload := context.WithTimeout(e.ctx, downloadTimeout)
	a, err := e.choice.src.Fetch(downloadCtx, j.Request, e.role, e.arch)
	cancelDownload()
	if err != nil {
		return err
	}
	data, err := Accept(e.keys, e.role, e.arch, j.Version, a)
	if err != nil {
		return err
	}
	if time.Now().Unix() >= j.ExpiresAt {
		return errors.New("update expired before installation")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Minute)
	defer cancel()
	digest, err := e.machine.Stage(data)
	if err != nil {
		return err
	}
	e.mu.Lock()
	next := *e.job
	next.Digest = digest
	err = e.save(next)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if err = e.transition("stopping"); err != nil {
		return err
	}
	if err = e.machine.Gate(true); err != nil {
		return err
	}
	if err = e.machine.Stop(ctx); err != nil {
		return err
	}
	if err = e.machine.Backup(); err != nil {
		return err
	}
	// installing 的持久化证明备份完整，恢复路径才能使用它。
	if err = e.transition("installing"); err != nil {
		return err
	}
	if err = e.machine.Install(); err != nil {
		return err
	}
	if err = e.transition("verifying"); err != nil {
		return err
	}
	if err = e.machine.Start(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(e.readyTimeout)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if e.status().Job.State == "succeeded" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("new process did not confirm readiness before timeout")
		case <-tick.C:
		}
	}
}

func (e *Engine) ready(ctx context.Context, pid int, version string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recoveryError != "" {
		return errors.New(e.recoveryError)
	}
	if e.job == nil || e.job.State != "verifying" {
		return errors.New("no update awaiting readiness")
	}
	if version != e.job.Version {
		return errors.New("ready version differs from target")
	}
	if err := e.machine.Verify(ctx, pid, e.job.Digest); err != nil {
		return err
	}
	j := *e.job
	j.State = "succeeded"
	if err := e.save(j); err != nil {
		return err
	}
	e.version = version
	if err := e.machine.Gate(false); err != nil {
		e.recoveryError = boundedError("clear committed update gate: " + err.Error())
		return err
	}
	return nil
}

// recover 在启动开放 socket 前或独占事务锁下运行，绝不与新任务共享备份。
func (e *Engine) recover() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	j := *e.job
	changed := j.State == "installing" || j.State == "verifying" || j.State == "rolling_back"
	if changed {
		// 恢复可能只写回部分文件；先撤销候选的就绪资格，重启后也只能继续回滚。
		j.State = "rolling_back"
		if err := e.save(j); err != nil {
			return err
		}
		if err := e.machine.Stop(ctx); err != nil {
			return fmt.Errorf("stop candidate: %w", err)
		}
		if err := e.machine.Restore(); err != nil {
			return fmt.Errorf("restore backup: %w", err)
		}
	}
	e.restart = changed || j.State == "stopping"
	j.State = "failed"
	if changed {
		j.State = "rolled_back"
	}
	j.Error = "updater interrupted or update failed"
	// 恢复结果先持久化再开放入口；之后中断只能重试启动，不能再用旧备份覆盖已接受的新写入。
	if err := e.save(j); err != nil {
		return err
	}
	if err := e.machine.Gate(false); err != nil {
		return err
	}
	if e.restart {
		if err := e.machine.Start(ctx); err != nil {
			return fmt.Errorf("restart original: %w", err)
		}
		e.restart = false
		return e.save(j)
	}
	return nil
}

func boundedError(s string) string {
	r := []rune(s)
	if len(r) > 512 {
		r = r[:512]
	}
	return string(r)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
