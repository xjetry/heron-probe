package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/probelimit"
)

var ErrBadOrder = errors.New("ids must list every node exactly once")

// BillingCycle 按 TEXT 落库，空串表示没有周期。与协议枚举的对应在 api 的 billingCycles 表，周期的月数在
// alert 的 cycleMonths；两处的测试都按 BillingCycles 核对一一对应。
type BillingCycle string

const (
	CycleNone       BillingCycle = ""
	CycleMonthly    BillingCycle = "monthly"
	CycleQuarterly  BillingCycle = "quarterly"
	CycleSemiannual BillingCycle = "semiannual"
	CycleYearly     BillingCycle = "yearly"
	CycleBiennial   BillingCycle = "biennial"
	CycleTriennial  BillingCycle = "triennial"
)

// BillingCycles 列出全部非空周期。
func BillingCycles() []BillingCycle {
	return []BillingCycle{CycleMonthly, CycleQuarterly, CycleSemiannual, CycleYearly, CycleBiennial, CycleTriennial}
}

// Billing 是节点的计费与到期（§9.4），随 UpdateNode 整体替换，零值即"都没填"。存储不校验取值：写入口有两个，
// api 的 UpdateNode 按 §9.4 校验整组取值，RenewExpiry 只写 alert 推后得到的日期。
type Billing struct {
	Price     string
	Currency  string
	Cycle     BillingCycle
	ExpiresOn string // YYYY-MM-DD；空表示没有到期日
	AutoRenew bool
}

// NodeEdit 是 UpdateNode 整体替换的可编辑字段；调用方已做校验与清洗。
type NodeEdit struct {
	Name            string
	Public          bool
	Note            string
	TrafficResetDay int
	OfflineGraceS   int // 0 写 NULL，读侧取 TTL
	Billing         Billing
}

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
	Billing        Billing
}

const selectNodes = `SELECT n.id, n.name, n.public, n.note, n.sort_order, n.created_at, n.last_seen_at, n.traffic_reset_day, n.offline_grace_s,
	n.price, n.currency, n.billing_cycle, n.expires_on, n.auto_renew,
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
		b := &n.Billing
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &n.SortOrder, &created, &seen, &n.TrafficResetDay, &grace,
			&b.Price, &b.Currency, &b.Cycle, &b.ExpiresOn, &b.AutoRenew,
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

// UpdateNode 整体替换可编辑字段，不存在"不改"的取值。billingChanged 报告计费五项与写入前的库内值是否不同，
// 调用方据它决定是否立即做一次到期扫描。库内值在同一个写事务里读出，RenewExpiry 也经单写协程，读与写之间插不进
// 一次推后，所以它就是这次写入实际覆盖掉的值。表单若带着推后之前的到期日提交，库内值已是推后的日期，两者不同，
// 调用方随即重新扫描、再推后一次。
func (s *Store) UpdateNode(ctx context.Context, id int64, e NodeEdit) (billingChanged bool, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		var old Billing
		err := tx.QueryRow("SELECT price, currency, billing_cycle, expires_on, auto_renew FROM node WHERE id = ?", id).
			Scan(&old.Price, &old.Currency, &old.Cycle, &old.ExpiresOn, &old.AutoRenew)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		b := e.Billing
		if _, err := tx.Exec(`UPDATE node SET name = ?, public = ?, note = ?, traffic_reset_day = ?, offline_grace_s = NULLIF(?, 0),
			price = ?, currency = ?, billing_cycle = ?, expires_on = ?, auto_renew = ? WHERE id = ?`,
			e.Name, e.Public, e.Note, e.TrafficResetDay, e.OfflineGraceS, b.Price, b.Currency, b.Cycle, b.ExpiresOn, b.AutoRenew, id); err != nil {
			return err
		}
		billingChanged = old != b
		return nil
	})
	if err != nil {
		return false, err
	}
	return billingChanged, nil
}

// RenewExpiry 把自动续期推后的到期日写回，前提是该行此刻仍是推后所依据的那组取值（开着自动续期、周期与
// 旧到期日都没变）：推后的日期由到期扫描从它读出的快照算出，快照之后 UpdateNode 若改了计费字段，按旧快照写回
// 就会盖掉管理员刚保存的值。条件不成立时不写、返回 false：计费被 UpdateNode 改过时，由那次 UpdateNode 触发的扫描
// 按新值重算；节点已被删除时，没有要重算的对象。
func (s *Store) RenewExpiry(ctx context.Context, id int64, cycle BillingCycle, from, to string) (bool, error) {
	var renewed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET expires_on = ? WHERE id = ? AND auto_renew = 1 AND billing_cycle = ? AND expires_on = ?", to, id, cycle, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		renewed = n == 1
		return err
	})
	return renewed, err
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

// NewNodeTasks 是建节点事务提交时新节点的探测清单：Version 是该事务推进后的任务版本，TaskIDs 是同一事务里按
// probeCoverage 读出的新节点覆盖，升序。探测任务注册表的建节点增量只取这份结果，不另行推导覆盖。
type NewNodeTasks struct {
	Version uint64
	TaskIDs []uint64
}

// CreateNode 返回新节点的 id 与它在建节点事务提交时的探测清单（见 insertNode）。
func (s *Store) CreateNode(ctx context.Context, name string, tokenHash []byte) (int64, NewNodeTasks, error) {
	var id int64
	var tasks NewNodeTasks
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		id, tasks, err = insertNode(tx, name, tokenHash, s.clk.Now().Unix())
		return err
	})
	return id, tasks, err
}

// insertNode 是两个创建入口（CreateNode 与 RegisterNode）共用的建节点步骤，在调用方的写事务里完成：
//   - 分配末尾序号，重排后的相对顺序不被新节点打断；
//   - 按 probeCoverage 读出新节点覆盖的任务 id：读在本事务写入节点行之后，事务从那次写入起持有库的写锁直到提交，
//     别的写入插不进来，所以返回的清单就是提交时库里新节点的覆盖；
//   - 检查每节点任务上限：新节点继承全部 all_nodes 任务，SaveProbeTask 的上限检查只覆盖保存那一刻已有的节点，
//     没有节点时保存的 all_nodes 任务可以超过上限，所以建节点这一侧必须再查；超限返回 InheritedLimitError，
//     事务回滚，节点不建；
//   - 推进任务版本：新节点的清单从空变为全部 all_nodes 任务，按 bumpProbeVersion 的不变式必须推进。
func insertNode(tx *sql.Tx, name string, tokenHash []byte, createdAt int64) (int64, NewNodeTasks, error) {
	res, err := tx.Exec(`INSERT INTO node (name, token_hash, created_at, sort_order)
		SELECT ?, ?, ?, COALESCE(MAX(sort_order), -1) + 1 FROM node`, name, tokenHash, createdAt)
	if err != nil {
		return 0, NewNodeTasks{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, NewNodeTasks{}, err
	}
	covered, err := scanIDs(tx.Query("SELECT task_id FROM ("+probeCoverage+") WHERE node_id = ? ORDER BY task_id", id))
	if err != nil {
		return 0, NewNodeTasks{}, err
	}
	if len(covered) > probelimit.MaxTasksPerNode {
		return 0, NewNodeTasks{}, InheritedLimitError{Tasks: len(covered), Max: probelimit.MaxTasksPerNode}
	}
	version, err := bumpProbeVersion(tx, createdAt)
	if err != nil {
		return 0, NewNodeTasks{}, err
	}
	tasks := NewNodeTasks{Version: uint64(version)}
	for _, task := range covered {
		tasks.TaskIDs = append(tasks.TaskIDs, uint64(task))
	}
	return id, tasks, nil
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
