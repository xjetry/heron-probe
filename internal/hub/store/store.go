// Package store 是 hub 唯一的持久化层：单文件 SQLite，纯 Go 驱动。
//
// 不变式：所有写都经 runWriter 串行执行，w 只在那个协程里被使用。SQLite 同一
// 时刻只允许一个写者，应用内串行化从根上避免写者之间的 SQLITE_BUSY；读走
// 独立的只读连接池（query_only），WAL 下读不阻塞写。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"

	"github.com/xjetry/probe/internal/clock"
)

var (
	ErrNotFound = errors.New("not found")
	ErrNoWindow = errors.New("register window closed")
	ErrBadKey   = errors.New("register key mismatch")
)

type Store struct {
	w      *sql.DB
	r      *sql.DB
	clk    clock.Clock
	log    *slog.Logger
	writes chan writeReq
	done   chan struct{}
}

type writeReq struct {
	fn func(*sql.Tx) error
	// res 为 nil 表示调用方不等结果（投递即返回）。
	res chan error
}

func dsn(path string, extra string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" + extra
}

func Open(path string, clk clock.Clock, log *slog.Logger) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := migrate(w); err != nil {
		w.Close()
		return nil, err
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
	close(s.writes)
	<-s.done
	return errors.Join(s.r.Close(), s.w.Close())
}

func (s *Store) runWriter() {
	defer close(s.done)
	for req := range s.writes {
		err := s.inTx(req.fn)
		if req.res != nil {
			req.res <- err
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

// write 投递一个事务并等待其结果。ctx 取消只放弃等待：已入队的事务仍会执行。
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	req := writeReq{fn: fn, res: make(chan error, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeAsync 投递后立即返回；done 在写协程里被调用。队列满时丢弃并报告，
// 调用方据此保持自己的状态不变，让下一次上报重新触发。
func (s *Store) writeAsync(fn func(*sql.Tx) error, done func(error)) {
	req := writeReq{fn: func(tx *sql.Tx) error {
		err := fn(tx)
		if done != nil {
			defer done(err)
		}
		return err
	}}
	select {
	case s.writes <- req:
	default:
		s.log.Warn("write queue full, dropping async write")
		if done != nil {
			done(errors.New("write queue full"))
		}
	}
}

const schemaVersion = 1

// migrations[v] 把 user_version = v−1 的库升到 v。空库不重放历史，直接建
// 到当前版本；所以 schema 常量必须始终是"当前版本的完整 DDL"。
var migrations = map[int]func(*sql.Tx) error{}

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
