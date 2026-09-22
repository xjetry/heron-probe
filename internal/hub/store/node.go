package store

import (
	"context"
	"database/sql"
	"time"
)

type Node struct {
	ID         int64
	Name       string
	Public     bool
	Note       string
	CreatedAt  time.Time
	LastSeenAt time.Time // 零值表示从未上报
}

func (s *Store) CreateNode(ctx context.Context, name string, tokenHash []byte) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("INSERT INTO node (name, token_hash, created_at) VALUES (?, ?, ?)",
			name, tokenHash, s.clk.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.r.QueryContext(ctx,
		"SELECT id, name, public, note, created_at, last_seen_at FROM node ORDER BY sort_order, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var created int64
		var seen sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &created, &seen); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(created, 0).UTC()
		if seen.Valid {
			n.LastSeenAt = time.Unix(seen.Int64, 0).UTC()
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteNode 显式删除从属行。不依赖外键：SQLite 默认不开启外键约束，
// 而备份恢复是整表覆盖、不触发级联；靠显式删除才在两条路径上都成立。
func (s *Store) DeleteNode(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM node WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, q := range []string{
			"DELETE FROM node_facts WHERE node_id = ?",
			"DELETE FROM metric_1m WHERE node_id = ?",
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SetTokenHash(ctx context.Context, id int64, hash []byte) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET token_hash = ? WHERE id = ?", hash, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) TokenHashes(ctx context.Context) (map[[32]byte]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, token_hash FROM node")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[32]byte]int64{}
	for rows.Next() {
		var id int64
		var h []byte
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		var k [32]byte
		copy(k[:], h)
		out[k] = id
	}
	return out, rows.Err()
}

// Counts 返回各表行数，供运维核对与端到端验收。
func (s *Store) Counts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"node", "node_facts", "metric_1m", "register_window"} {
		var n int64
		if err := s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}
