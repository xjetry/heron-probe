// Package store 是 hub 唯一的持久化层：单文件 SQLite，纯 Go 驱动。
//
// 不变式：所有写都经 runWriter 串行执行，w 只在那个协程里被使用。SQLite 同一
// 时刻只允许一个写者，应用内串行化从根上避免写者之间的 SQLITE_BUSY；读走
// 独立的只读连接池（query_only），WAL 下读不阻塞写。
// 写请求返回错误意味着事务未应用，返回 nil 意味着已提交；这是 auth 只在
// 写成功后更新内存映射、保持映射与库一致的前提。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite"

	"github.com/xjetry/heron-probe/internal/clock"
)

var (
	ErrClosed   = errors.New("store closed")
	ErrNotFound = errors.New("not found")
	ErrNoWindow = errors.New("register window closed")
	ErrBadKey   = errors.New("register key mismatch")
)

type Store struct {
	path string
	// siteWriteMu 让设置的提交与总闸发布对其它保存原子，维持的不变式写在 SaveSettings。
	siteWriteMu   sync.Mutex
	publicEnabled atomic.Bool
	closeMu       sync.RWMutex
	closed        bool
	w             *sql.DB
	r             *sql.DB
	clk           clock.Clock
	log           *slog.Logger
	writes        chan writeReq
	done          chan struct{}
	// themeGen 是主题版本与全站选择的代数；在线写者统一经 writeTheme 推进。
	themeGen     atomic.Uint64
	themeChanges chan struct{}
}

type writeReq struct {
	ctx context.Context // 异步请求为 nil，不受取消影响。
	fn  func(*sql.Tx) error
	// runWriter 仅在请求因取消未执行或 inTx 返回最终结果后通知 res 或 done；
	// 已开始事务的提交或回滚先于通知，回调收到 nil 时读到的是已持久化的状态。
	res  chan error
	done func(error)
}

// dsn 不含 journal_mode(WAL)：这个 pragma 一旦生效就立即改写文件头（第 18—19 字节
// 标出日志模式），比 migrate 读版本号、判定"这是不是本项目的库"更早。所有连接都带它
// 会让身份判定本身成为一次写：外来库被拒绝时文件也已经被改成 WAL，判定分支之后没有
// 机会撤销。WAL 只在 migrate 判定通过、库确实建成或迁移完成后由写连接显式打开，
// 见 openStore；一旦打开，日志模式记在文件里，之后的连接（包括这里的读连接池）
// 不需要也不应重复声明它。
func dsn(path string, extra string) string {
	// path 是文件名而非 URI；编码路径部分，避免 #、? 和 % 改变实际打开的数据库。
	u := url.URL{Path: path}
	return "file:" + u.EscapedPath() + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" + extra
}

// SchemaPolicy 由打开库的入口显式选择。离线命令不得迁移旧库：否则用新二进制查看 stats
// 就会单向升级数据库，而 migrate 的版本检查会让旧 hub 在下次启动时拒绝打开它。
// 零值非法，避免调用方遗漏选择时意外迁移。
type SchemaPolicy int

const (
	MigrateSchema SchemaPolicy = iota + 1
	RequireCurrentSchema
)

// Open 按 policy 检查或迁移 schema。SQLite 打开失败的报错不带文件名（如 unable to open database file (14)）；
// serve 与离线子命令都经这里打开库，打开过程的每一种失败都在这一层补上路径，报错才指得出是哪个文件、
// 该查哪个目录的权限。
func Open(path string, clk clock.Clock, log *slog.Logger, policy SchemaPolicy) (*Store, error) {
	if policy != MigrateSchema && policy != RequireCurrentSchema {
		panic("store.Open requires a valid SchemaPolicy")
	}
	s, err := openStore(path, clk, log, policy)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return s, nil
}

func openStore(path string, clk clock.Clock, log *slog.Logger, policy SchemaPolicy) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := migrate(w, policy, log); err != nil {
		w.Close()
		return nil, err
	}
	// migrate 返回 nil 意味着这是本项目的库（已在当前版本、刚建成、或刚迁移完成）；
	// 只有到这里才把日志模式切到 WAL，拒绝分支在此之前已经返回，不会执行到这一行。
	if _, err := w.Exec("PRAGMA journal_mode=WAL"); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, "&_pragma=query_only(1)"))
	if err != nil {
		w.Close()
		return nil, err
	}
	s := &Store{path: path, w: w, r: r, clk: clk, log: log, writes: make(chan writeReq, 1024), done: make(chan struct{}), themeChanges: make(chan struct{}, 1)}
	// 打开时读一次设置，同时满足两件事：总闸的内存副本从库加载（不变式见 SaveSettings）；设置里有必须合法才能解释的
	// 编码（两个开关只认 0 / 1，备份的数值有范围，渠道列表是 JSON 数组，见 readSettings），库里有非法值就拒绝打开。
	settings, err := readSettings(context.Background(), r)
	if err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	s.publicEnabled.Store(settings.Site.PublicEnabled)
	go s.runWriter()
	return s, nil
}

// Close 等待队列里的写全部执行完再关闭连接，退出时投递的最后一批刷出不丢。
func (s *Store) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.writes)
	<-s.done
	return errors.Join(s.r.Close(), s.w.Close())
}

func (s *Store) runWriter() {
	defer close(s.done)
	for req := range s.writes {
		var err error
		if req.ctx != nil && req.ctx.Err() != nil {
			err = req.ctx.Err()
		} else {
			err = s.inTx(req.fn)
		}
		if req.res != nil {
			req.res <- err
		} else if req.done != nil {
			req.done(err)
		} else if err != nil {
			s.log.Error("async write failed", "err", err)
		}
	}
}

func (s *Store) inTx(fn func(*sql.Tx) error) error {
	tx, err := s.w.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// write 返回错误意味着事务未应用，返回 nil 意味着已提交；入队后必须等到
// runWriter 给出最终结果。中途放弃等待会让调用方在事务照常提交时误以为失败，
// 据此不更新内存映射就会造成映射与库分叉。取消只阻止尚未开始的事务。
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	change, _ := ctx.Value(changeKey{}).(*Change)
	activeChange := change != nil && !change.completed
	if activeChange {
		fn = s.changeWrite(ctx, change, fn)
	}
	s.closeMu.RLock()
	if s.closed {
		s.closeMu.RUnlock()
		return ErrClosed
	}
	// runWriter 在通道关闭前持续消费，满队列下的投递仍能完成并释放读锁；
	// Close 获得写锁后才关闭通道，因此不会因等待此读锁而阻止队列消费。
	req := writeReq{ctx: ctx, fn: fn, res: make(chan error, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		s.closeMu.RUnlock()
		return ctx.Err()
	}
	s.closeMu.RUnlock()
	err := <-req.res
	if activeChange && err == nil {
		change.completed = true
	}
	if activeChange && err != nil && !errors.Is(err, ErrReplay) {
		change.CommittedAt = 0
	}
	return err
}

// writeAsync 投递后立即返回；done 在写协程里被调用。队列满时丢弃并报告，
// 调用方据此保持自己的状态不变，让下一次上报重新触发。
func (s *Store) writeAsync(fn func(*sql.Tx) error, done func(error)) {
	s.closeMu.RLock()
	if s.closed {
		s.closeMu.RUnlock()
		if done != nil {
			done(ErrClosed)
		}
		return
	}
	req := writeReq{fn: fn, done: done}
	select {
	case s.writes <- req:
		s.closeMu.RUnlock()
	default:
		s.closeMu.RUnlock()
		s.log.Warn("write queue full, dropping async write")
		if done != nil {
			done(errors.New("write queue full"))
		}
	}
}

const schemaVersion = 26

type schemaAction int

const (
	schemaCurrent schemaAction = iota
	schemaCreate
	schemaMigrate
)

type schemaReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// schemaAdmission 只读取版本与对象数；Open 与 Restore 在任何建表或迁移之前共用这一判定。
func schemaAdmission(ctx context.Context, db schemaReader, policy SchemaPolicy) (schemaAction, int, error) {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA main.user_version").Scan(&v); err != nil {
		return 0, v, err
	}
	switch {
	case v == schemaVersion:
		return schemaCurrent, v, nil
	case v > schemaVersion:
		return 0, v, fmt.Errorf("database schema version %d is newer than this binary (%d)", v, schemaVersion)
	case v < 0:
		// createSchema 写 schemaVersion，migrate 从已准入的正版本逐步写 next，两处都不产生负数。
		// 把负数当旧库交给迁移循环会查 migrations[v+1]（v=-1 时找不到 migrations[0]），
		// 报出与升级无关的内部错误；当空库建表则会把 schemaStatements 叠加到未知内容上。
		// 因而负版本既不能迁移，也不能按空库放行。
		return 0, v, fmt.Errorf("database schema version %d is invalid; not a Heron database", v)
	case v == 0:
		// PRAGMA user_version 未显式设置时读出的也是 0，任何 SQLite 文件都满足这一条；
		// 只有 sqlite_schema 里确实不存在任何对象才是 §6.6 定义的"空库"。有对象却没有版本号
		// 说明这是别的程序建的库，在它上面叠加 schemaStatements 会把两套 schema 的对象混进
		// 同一个文件——stats --db 指错文件时就会把陌生库当空库建满全部表。
		var objects int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_schema").Scan(&objects); err != nil {
			return 0, v, err
		}
		if objects > 0 {
			return 0, v, errors.New("database has tables but no schema version; not a Heron database")
		}
		return schemaCreate, v, nil
	case policy == RequireCurrentSchema:
		return 0, v, fmt.Errorf("database schema version %d is older than this binary (%d); start the new heron-hub serve once to upgrade it (back up the database first)", v, schemaVersion)
	}
	return schemaMigrate, v, nil
}

// createSchema 使用调用方的事务，使恢复时建库与数据覆盖一并提交或回滚。
func createSchema(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range schemaStatements() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w in %q", err, stmt)
		}
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA main.user_version = %d", schemaVersion))
	return err
}

func logSchemaCreated(log *slog.Logger) {
	log.Info("database schema created", "version", schemaVersion)
}

// migrate 用 user_version 保存当前版本，不在库里保存迁移历史或时间；运维需要从日志判断
// 何时迁过、该还原哪份备份。因此每步事务提交成功后都记日志，避免遗漏步骤或把回滚记成已完成。
func migrate(db *sql.DB, policy SchemaPolicy, log *slog.Logger) error {
	ctx := context.Background()
	action, v, err := schemaAdmission(ctx, db, policy)
	if err != nil {
		return err
	}
	switch action {
	case schemaCurrent:
		return nil
	case schemaCreate:
		if err := inTxDB(db, func(tx *sql.Tx) error { return createSchema(ctx, tx) }); err != nil {
			return err
		}
		logSchemaCreated(log)
		return nil
	}
	for next := v + 1; next <= schemaVersion; next++ {
		step, ok := migrations[next]
		if !ok {
			return fmt.Errorf("no migration to schema version %d", next)
		}
		if err := inTxDB(db, func(tx *sql.Tx) error {
			if err := step(tx); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", next))
			return err
		}); err != nil {
			return err
		}
		log.Info("database schema migrated", "from", next-1, "to", next)
	}
	return nil
}

func inTxDB(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
