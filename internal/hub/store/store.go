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

	_ "modernc.org/sqlite"

	"github.com/xjetry/probe/internal/clock"
)

var (
	ErrClosed   = errors.New("store closed")
	ErrNotFound = errors.New("not found")
	ErrNoWindow = errors.New("register window closed")
	ErrBadKey   = errors.New("register key mismatch")
)

type Store struct {
	closeMu sync.RWMutex
	closed  bool
	w       *sql.DB
	r       *sql.DB
	clk     clock.Clock
	log     *slog.Logger
	writes  chan writeReq
	done    chan struct{}
}

type writeReq struct {
	ctx context.Context // 异步请求为 nil，不受取消影响。
	fn  func(*sql.Tx) error
	// runWriter 仅在请求因取消未执行或 inTx 返回最终结果后通知 res 或 done；
	// 已开始事务的提交或回滚先于通知，回调收到 nil 时读到的是已持久化的状态。
	res  chan error
	done func(error)
}

func dsn(path string, extra string) string {
	// path 是文件名而非 URI；编码路径部分，避免 #、? 和 % 改变实际打开的数据库。
	u := url.URL{Path: path}
	return "file:" + u.EscapedPath() + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" + extra
}

func Open(path string, clk clock.Clock, log *slog.Logger) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := migrate(w); err != nil {
		w.Close()
		// SQLite 打开失败的报错不带文件名（如 unable to open database file (14)）；serve 与离线子命令
		// 都经这里打开库，在这一层补上路径，报错才指得出是哪个文件、该查哪个目录的权限。
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	r, err := sql.Open("sqlite", dsn(path, "&_pragma=query_only(1)"))
	if err != nil {
		w.Close()
		return nil, err
	}
	s := &Store{w: w, r: r, clk: clk, log: log, writes: make(chan writeReq, 1024), done: make(chan struct{})}
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
	return <-req.res
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

const schemaVersion = 7

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		return nil
	case v > schemaVersion:
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, schemaVersion)
	case v == 0:
		return inTxDB(db, func(tx *sql.Tx) error {
			for _, stmt := range schemaStatements() {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("%w in %q", err, stmt)
				}
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
			return err
		})
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
