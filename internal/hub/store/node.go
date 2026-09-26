package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

var ErrBadOrder = errors.New("ids must list every node exactly once")

type Node struct {
	ID              int64
	Name            string
	Public          bool
	Note            string
	SortOrder       int32
	CreatedAt       time.Time
	LastSeenAt      time.Time // 零值表示从未上报
	TrafficResetDay int       // 周期重置日 1–28，列默认 1
	OfflineGraceS   int       // 0 表示列为 NULL，读侧取 TTL。
	// Facts 为 nil 表示该节点尚未上报过静态信息。
	Facts          *probev1.Facts
	FactsUpdatedAt time.Time
}

const selectNodes = `SELECT n.id, n.name, n.public, n.note, n.sort_order, n.created_at, n.last_seen_at, n.traffic_reset_day, n.offline_grace_s,
	f.hostname, f.os, f.kernel, f.arch, f.virtualization, f.cpu_model, f.cpu_cores, f.agent_version, f.icmp_available, f.updated_at
	FROM node n LEFT JOIN node_facts f ON f.node_id = n.id`

// nodeOrder 是节点列表唯一的排序：面板与公开页看到同一个顺序。
const nodeOrder = " ORDER BY n.sort_order, n.id"

func scanNodes(rows *sql.Rows) ([]Node, error) {
	var out []Node
	for rows.Next() {
		var n Node
		var created int64
		var seen, grace sql.NullInt64
		var hostname, os, kernel, arch, virt, cpuModel, agentVersion sql.NullString
		var cores, icmp, factsUpdated sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &n.SortOrder, &created, &seen, &n.TrafficResetDay, &grace,
			&hostname, &os, &kernel, &arch, &virt, &cpuModel, &cores, &agentVersion, &icmp, &factsUpdated); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(created, 0).UTC()
		n.OfflineGraceS = int(grace.Int64)
		if seen.Valid {
			n.LastSeenAt = time.Unix(seen.Int64, 0).UTC()
		}
		if factsUpdated.Valid {
			n.Facts = &probev1.Facts{
				Hostname: hostname.String, Os: os.String, Kernel: kernel.String, Arch: arch.String,
				Virtualization: virt.String, CpuModel: cpuModel.String, CpuCores: uint32(cores.Int64),
				AgentVersion: agentVersion.String, IcmpAvailable: icmp.Int64 != 0,
			}
			n.FactsUpdatedAt = time.Unix(factsUpdated.Int64, 0).UTC()
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.r.QueryContext(ctx, selectNodes+nodeOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ListPublicNodes 只返回 public = 1 的节点。公开服务只经它与 NodeIsPublic 读节点，可见范围由这两处承载：
// 这里的 WHERE n.public = 1，与 NodeIsPublic 读出的 public 列（不存在的 id 同样得到 false）。
func (s *Store) ListPublicNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.r.QueryContext(ctx, selectNodes+" WHERE n.public = 1"+nodeOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// NodeIsPublic 对未公开的节点与不存在的节点同样返回 false：调用方无从、也不需要区分二者。
func (s *Store) NodeIsPublic(ctx context.Context, id int64) (bool, error) {
	var public bool
	err := s.r.QueryRowContext(ctx, "SELECT public FROM node WHERE id = ?", id).Scan(&public)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return public, err
}

func (s *Store) GetNode(ctx context.Context, id int64) (Node, error) {
	rows, err := s.r.QueryContext(ctx, selectNodes+" WHERE n.id = ?", id)
	if err != nil {
		return Node{}, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return Node{}, err
	}
	if len(nodes) == 0 {
		return Node{}, ErrNotFound
	}
	return nodes[0], nil
}

func (s *Store) NodeExists(ctx context.Context, id int64) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, "SELECT 1 FROM node WHERE id = ?", id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// UpdateNode 整体替换可编辑字段；调用方已做校验与清洗。重置日与其他字段一起整体替换，
// 不存在"不改"的取值。
func (s *Store) UpdateNode(ctx context.Context, id int64, name string, public bool, note string, resetDay int, offlineGraceS int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET name = ?, public = ?, note = ?, traffic_reset_day = ?, offline_grace_s = NULLIF(?, 0) WHERE id = ?", name, public, note, resetDay, offlineGraceS, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ReorderNodes 在写事务中验证 ids 恰是全部节点的一个排列，再整体更新顺序。
// 部分列表可能让未列出的节点与列出的节点共用 sort_order，转而由 id 决定相对次序。
func (s *Store) ReorderNodes(ctx context.Context, ids []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT id FROM node")
		if err != nil {
			return err
		}
		existing := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) != len(existing) {
			return ErrBadOrder
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if !existing[id] || seen[id] {
				return ErrBadOrder
			}
			seen[id] = true
		}
		for i, id := range ids {
			if _, err := tx.Exec("UPDATE node SET sort_order = ? WHERE id = ?", i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNode 在同一写事务中显式删除节点及从属行；schema 未声明级联外键，
// 因而清理必须由本函数完成，不依赖连接是否开启外键约束。
func (s *Store) DeleteNode(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM node WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec("DELETE FROM node_facts WHERE node_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM traffic WHERE node_id = ?", id); err != nil {
			return err
		}
		for _, t := range metricTables {
			if _, err := tx.Exec("DELETE FROM "+t+" WHERE node_id = ?", id); err != nil {
				return err
			}
		}
		for _, t := range probeTables {
			if _, err := tx.Exec("DELETE FROM "+t+" WHERE node_id = ?", id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM probe_task_node WHERE node_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM alert_state WHERE node_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM alert_rule_node WHERE node_id = ?", id); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) CreateNode(ctx context.Context, name string, tokenHash []byte) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = insertNode(tx, name, tokenHash, s.clk.Now().Unix())
		return err
	})
	return id, err
}

// 两个创建入口共用事务内的末尾序号分配，重排后的相对顺序不被新节点打断。
func insertNode(tx *sql.Tx, name string, tokenHash []byte, createdAt int64) (int64, error) {
	res, err := tx.Exec(`INSERT INTO node (name, token_hash, created_at, sort_order)
		SELECT ?, ?, ?, COALESCE(MAX(sort_order), -1) + 1 FROM node`, name, tokenHash, createdAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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

// nodeExistsTx 与从属行写入共用事务，由单写协程保证删除后排队的写入不能重建孤儿行。
func nodeExistsTx(tx *sql.Tx, id int64) (bool, error) {
	var exists bool
	err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM node WHERE id = ?)", id).Scan(&exists)
	return exists, err
}
