package store

import (
	"bytes"
	"context"
	"database/sql"
	"time"
)

type Window struct {
	ExpiresAt time.Time
	Remaining int
}

// SetRegisterWindow 替换当前窗口：同一时刻只有一个窗口，新开即作废旧 key。
func (s *Store) SetRegisterWindow(ctx context.Context, keyHash []byte, expiresAt time.Time, maxNodes int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO register_window (id, key_hash, expires_at, remaining) VALUES (1, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET key_hash = excluded.key_hash, expires_at = excluded.expires_at, remaining = excluded.remaining`,
			keyHash, expiresAt.Unix(), maxNodes)
		return err
	})
}

func (s *Store) ClearRegisterWindow(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM register_window")
		return err
	})
}

// RegisterWindow 返回窗口原样，不判过期：判定属于 RegisterNode 的事务，
// 展示侧只需要把截止时间给人看。
func (s *Store) RegisterWindow(ctx context.Context) (Window, bool, error) {
	var w Window
	var exp int64
	err := s.r.QueryRowContext(ctx, "SELECT expires_at, remaining FROM register_window WHERE id = 1").Scan(&exp, &w.Remaining)
	if err == sql.ErrNoRows {
		return Window{}, false, nil
	}
	if err != nil {
		return Window{}, false, err
	}
	w.ExpiresAt = time.Unix(exp, 0).UTC()
	return w, true, nil
}

// RegisterNode 在一个事务里判定窗口、比对 key、建节点、消耗名额。
//
// ErrNoWindow 覆盖"没有窗口 / 已过期 / 名额用尽"，ErrBadKey 只表示窗口开着
// 但 key 不对：调用方对外把两者映射成同一响应，但只对后者计失败次数——
// 窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。
func (s *Store) RegisterNode(ctx context.Context, keyHash []byte, name string, tokenHash []byte) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var stored []byte
		var exp int64
		var remaining int
		err := tx.QueryRow("SELECT key_hash, expires_at, remaining FROM register_window WHERE id = 1").Scan(&stored, &exp, &remaining)
		if err == sql.ErrNoRows {
			return ErrNoWindow
		}
		if err != nil {
			return err
		}
		if s.clk.Now().Unix() >= exp || remaining <= 0 {
			return ErrNoWindow
		}
		if !bytes.Equal(stored, keyHash) {
			return ErrBadKey
		}
		if id, err = insertNode(tx, name, tokenHash, s.clk.Now().Unix()); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE register_window SET remaining = remaining - 1 WHERE id = 1")
		return err
	})
	return id, err
}
