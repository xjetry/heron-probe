package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/probelimit"
)

var ErrBadOrder = errors.New("ids must list every item exactly once")

// BillingCycle 按 TEXT 落库，空串表示没有周期。与协议枚举的对应在 api 的 billingCycles 表，周期的月数在
// alert 的 cycleMonths；两处的测试都按 BillingCycles 核对一一对应。
type BillingCycle string

const (
	CycleNone         BillingCycle = ""
	CycleMonthly      BillingCycle = "monthly"
	CycleQuarterly    BillingCycle = "quarterly"
	CycleSemiannual   BillingCycle = "semiannual"
	CycleYearly       BillingCycle = "yearly"
	CycleBiennial     BillingCycle = "biennial"
	CycleTriennial    BillingCycle = "triennial"
	CycleQuinquennial BillingCycle = "quinquennial"
)

// BillingCycles 列出全部非空周期。
func BillingCycles() []BillingCycle {
	return []BillingCycle{CycleMonthly, CycleQuarterly, CycleSemiannual, CycleYearly, CycleBiennial, CycleTriennial, CycleQuinquennial}
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
	CountryPin      string // 手动指定的国家，空串表示不指定（回落到查得值）
	// Tags 整体替换节点的标签集合，空即清空。调用方已按 §10 校验每个名字、按 TagFold 去重并限定个数；
	// 重复的名字会撞 node_tag 的主键而让整次更新失败。
	Tags []string
}

type Node struct {
	ID              int64
	Name            string
	Public          bool
	Note            string
	SortOrder       int32
	CreatedAt       time.Time
	LastSeenAt      time.Time // 零值表示从未上报
	LastSource      string    // 最近一次上报的来源地址；空串表示 hub 没有记录到来源，含义见 node.last_source
	TrafficResetDay int       // 周期重置日 1–28，列默认 1
	OfflineGraceS   int       // 0 表示列为 NULL，读侧取 TTL。
	// Facts 为 nil 表示该节点尚未上报过静态信息。
	Facts          *heronv1.Facts
	FactsUpdatedAt time.Time
	Billing        Billing
	// Country 是对 CountryIP 这个地址的查询答案，两者同空同非空；CountryPin 是手动指定的国家。见 node 表的列注释。
	Country    string
	CountryIP  string
	CountryPin string
	// Tags 是节点的标签名（先建的写法），按 TagFold 排序；没有标签时为 nil。
	Tags []string
}

// CountrySource 是显示值的来源。
type CountrySource int

const (
	CountryNone CountrySource = iota
	CountryManual
	CountryLookup
)

// IsCountryCode 报告 s 是否恰为两个 ASCII 大写字母：node 表两处国家（查得的 country 与手动的 country_pin）的取值域，
// 查询应答（geo）、手动指定（api 的 UpdateNode）与 SetLookupCountry 共用这一个判定。只接受这一种形状：应答来自
// 第三方，收窄到 [A-Z]{2} 之后它不可能携带标记、文字或别的国家写法（小写、三字母、名称），应答体也就不进入任何
// 解释路径；页面按这两个字母算区域指示符旗帜，计算只对 A–Z 有定义。不核对是否是已分配的 ISO 3166-1 代码。
func IsCountryCode(s string) bool {
	return len(s) == 2 && isUpper(s[0]) && isUpper(s[1])
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// DisplayCountry 是面板与公开页显示的国家：手动值非空取手动值，否则取查得值。这是显示值唯一的判定，管理端与公开端
// 都经它取值。
func (n Node) DisplayCountry() (string, CountrySource) {
	switch {
	case n.CountryPin != "":
		return n.CountryPin, CountryManual
	case n.Country != "":
		return n.Country, CountryLookup
	}
	return "", CountryNone
}

const selectNodes = `SELECT n.id, n.name, n.public, n.note, n.sort_order, n.created_at, n.last_seen_at, n.traffic_reset_day, n.offline_grace_s,
	n.price, n.currency, n.billing_cycle, n.expires_on, n.auto_renew, n.last_source, n.country, n.country_ip, n.country_pin,
	f.hostname, f.os, f.kernel, f.arch, f.virtualization, f.cpu_model, f.cpu_cores, f.agent_version, f.icmp_available, f.updated_at, f.network, f.diagnostics
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
		var network, diagnostics sql.NullString
		var cores, icmp, factsUpdated sql.NullInt64
		b := &n.Billing
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &n.SortOrder, &created, &seen, &n.TrafficResetDay, &grace,
			&b.Price, &b.Currency, &b.Cycle, &b.ExpiresOn, &b.AutoRenew, &n.LastSource, &n.Country, &n.CountryIP, &n.CountryPin,
			&hostname, &os, &kernel, &arch, &virt, &cpuModel, &cores, &agentVersion, &icmp, &factsUpdated, &network, &diagnostics); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(created, 0).UTC()
		n.OfflineGraceS = int(grace.Int64)
		if seen.Valid {
			n.LastSeenAt = time.Unix(seen.Int64, 0).UTC()
		}
		if factsUpdated.Valid {
			n.Facts = &heronv1.Facts{
				Hostname: hostname.String, Os: os.String, Kernel: kernel.String, Arch: arch.String,
				Virtualization: virt.String, CpuModel: cpuModel.String, CpuCores: uint32(cores.Int64),
				AgentVersion: agentVersion.String, IcmpAvailable: icmp.Int64 != 0,
			}
			n.FactsUpdatedAt = time.Unix(factsUpdated.Int64, 0).UTC()
			info, err := decodeNetwork(network.String)
			if err != nil {
				return nil, fmt.Errorf("node %d network: %w", n.ID, err)
			}
			n.Facts.Network = info
			n.Facts.Diagnostics, err = decodeDiagnostics(diagnostics.String)
			if err != nil {
				return nil, fmt.Errorf("node %d diagnostics: %w", n.ID, err)
			}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) queryVisibleNodes(ctx context.Context, where string, args ...any) ([]Node, error) {
	if _, ok := Principal(ctx); ok {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += nodeScopeSQL(ctx, "n.id")
	}
	return s.queryNodes(ctx, where, args...)
}

// queryNodes 是 store.Node 值的唯一来源（selectNodes 与 scanNodes 只在这里用）：where 是作用于 node n 的条件（空串即
// 全部）。节点行与它们的标签在同一个只读事务里读出，两者来自同一个快照：两次独立查询之间插进一次 UpdateNode，节点行与
// 标签就会是不同时刻的样子（TestNodeRowAndTagsComeFromOneSnapshot）。
func (s *Store) queryNodes(ctx context.Context, where string, args ...any) ([]Node, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, selectNodes+where+nodeOrder, args...)
	if err != nil {
		return nil, err
	}
	nodes, err := scanNodes(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	tags, err := nodeTags(ctx, tx, where, args)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		nodes[i].Tags = tags[nodes[i].ID]
	}
	return nodes, nil
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	return s.queryVisibleNodes(ctx, "")
}

// ListMonitoringNodes 供全局监控评估使用，不按触发请求的凭据裁剪节点；告警规则与状态
// 属于整个引擎，若仅枚举调用者可见的节点，状态裁剪会把范围外节点误当成已删除。
func (s *Store) ListMonitoringNodes(ctx context.Context) ([]Node, error) {
	return s.queryNodes(ctx, "")
}

// ListPublicNodes 只返回 public = 1 的节点。公开服务只经它与 NodeIsPublic 读节点，可见范围由这两处承载：
// 这里的 WHERE n.public = 1，与 NodeIsPublic 读出的 public 列（不存在的 id 同样得到 false）。
// 返回的 Node 带着标签：公开快照据此公开公开节点的标签（§10），私有节点不在结果里，它的标签无从带出。
func (s *Store) ListPublicNodes(ctx context.Context) ([]Node, error) {
	return s.queryNodes(ctx, " WHERE n.public = 1")
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
	nodes, err := s.queryVisibleNodes(ctx, " WHERE n.id = ?", id)
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
	err := s.r.QueryRowContext(ctx, "SELECT 1 FROM node WHERE id = ? AND "+nodeScopeSQL(ctx, "node.id"), id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// UpdateNode 整体替换可编辑字段（含标签集合），不存在"不改"的取值。billingChanged 报告计费五项与写入前的库内值是否不同，
// 调用方据它决定是否立即做一次到期扫描。库内值在同一个写事务里读出，RenewExpiry 也经单写协程，读与写之间插不进
// 一次推后，所以它就是这次写入实际覆盖掉的值。表单若带着推后之前的到期日提交，库内值已是推后的日期，两者不同，
// 调用方随即重新扫描、再推后一次。
func (s *Store) UpdateNode(ctx context.Context, id int64, e NodeEdit) (billingChanged bool, err error) {
	result, err := s.UpdateNodeTasks(ctx, id, e)
	return result.BillingChanged, err
}

type NodeUpdateResult struct {
	BillingChanged bool
	Tasks          NewNodeTasks
	Rules          []AlertRule
}

// UpdateNodeTasks 在标签替换的同一事务内裁决任务上限、推进版本并读取新覆盖；注册表只发布这份已提交结果。
func (s *Store) UpdateNodeTasks(ctx context.Context, id int64, e NodeEdit) (result NodeUpdateResult, err error) {
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
			price = ?, currency = ?, billing_cycle = ?, expires_on = ?, auto_renew = ?, country_pin = ? WHERE id = ?`,
			e.Name, e.Public, e.Note, e.TrafficResetDay, e.OfflineGraceS, b.Price, b.Currency, b.Cycle, b.ExpiresOn, b.AutoRenew, e.CountryPin, id); err != nil {
			return err
		}
		if err := setNodeTags(tx, id, e.Tags); err != nil {
			return err
		}
		if err := checkProbeCoverageLimit(tx); err != nil {
			return err
		}
		if err := pruneAlertScopes(tx); err != nil {
			return err
		}
		ids, err := scanIDs(tx.Query("SELECT task_id FROM ("+probeCoverage+") WHERE node_id = ? ORDER BY task_id", id))
		if err != nil {
			return err
		}
		for _, taskID := range ids {
			result.Tasks.TaskIDs = append(result.Tasks.TaskIDs, uint64(taskID))
		}
		version, err := bumpProbeVersion(tx, s.clk.Now().Unix())
		if err != nil {
			return err
		}
		result.Tasks.Version = uint64(version)
		result.BillingChanged = old != b
		result.Rules, err = listAlertRulesTx(ctx, tx)
		return err
	})
	if err != nil {
		return NodeUpdateResult{}, err
	}
	return result, nil
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

// SetLookupCountry 写入对 addr 查得的国家，前提是节点的 last_source 此刻仍是 addr：查询在写协程之外发出，应答
// 到达之前节点可能已换了出口（WriteMinuteBatch 随之清空了两列），按旧地址的答案写回就会把旧出口的国家挂到新地址上。
// 条件不成立时不写、返回 false，新地址由查询器下一轮重查；节点已被删除时返回 ErrNotFound。
//
// country 与 country_ip 同空同非空（见 node 表的列注释）由这里的检查承载，不依赖调用方：addr 为空时条件
// last_source = addr 对从未上报的节点成立，会写出有国家没地址的一对；country 为空则写出有地址没国家的一对。
// 两种都返回错误、什么都不写，不是国家码的 country 同样拒绝。
func (s *Store) SetLookupCountry(ctx context.Context, id int64, addr, country string) (bool, error) {
	if addr == "" {
		return false, fmt.Errorf("lookup country of node %d: empty address", id)
	}
	if !IsCountryCode(country) {
		return false, fmt.Errorf("lookup country of node %d for %s: %q is not two uppercase letters", id, addr, country)
	}
	var set bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET country = ?, country_ip = ? WHERE id = ? AND last_source = ?", country, addr, id, addr)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if set = n == 1; set {
			return nil
		}
		var one int
		err = tx.QueryRow("SELECT 1 FROM node WHERE id = ?", id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return set, err
}

// ReorderNodes 在写事务中验证 ids 恰是全部节点的一个排列，再整体更新顺序。
// 部分列表可能让未列出的节点与列出的节点共用 sort_order，转而由 id 决定相对次序。
func (s *Store) ReorderNodes(ctx context.Context, ids []int64) error {
	return s.reorder(ctx, "node", ids)
}

// table 只来自本包固定调用点；完整集合校验和更新共用写事务，避免并发增删留下部分排列。
func (s *Store) reorder(ctx context.Context, table string, ids []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT id FROM " + table)
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
			if _, err := tx.Exec("UPDATE "+table+" SET sort_order = ? WHERE id = ?", i, id); err != nil {
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
		for _, t := range nodeDependentTables {
			if _, err := tx.Exec("DELETE FROM "+t+" WHERE node_id = ?", id); err != nil {
				return err
			}
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
// 管理端新建即公开：面板的「添加节点」不再单独询问公开范围，节点创建后默认进公开页，
// 需要隐藏时在编辑里关闭。RegisterNode（注册窗口）不经过这里，保持不公开。
func (s *Store) CreateNode(ctx context.Context, name string, tokenHash []byte) (int64, NewNodeTasks, error) {
	var id int64
	var tasks NewNodeTasks
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		id, tasks, err = insertNode(tx, name, tokenHash, s.clk.Now().Unix(), true)
		if err != nil {
			return err
		}
		return grantNode(tx, OwnerID(ctx), id)
	})
	return id, tasks, err
}

// insertNode 是两个创建入口（CreateNode 与 RegisterNode）共用的建节点步骤，在调用方的写事务里完成：
//   - 分配末尾序号，重排后的相对顺序不被新节点打断；public 由调用方给出（管理端 CreateNode 为 true，
//     RegisterNode 为 false），显式写入而不依赖列默认值；
//   - 按 probeCoverage 读出新节点覆盖的任务 id：读在本事务写入节点行之后，事务从那次写入起持有库的写锁直到提交，
//     别的写入插不进来，所以返回的清单就是提交时库里新节点的覆盖；
//   - 检查每节点任务上限：新节点继承全部 all_nodes 任务，SaveProbeTask 的上限检查只覆盖保存那一刻已有的节点，
//     没有节点时保存的 all_nodes 任务可以超过上限，所以建节点这一侧必须再查；超限返回 InheritedLimitError，
//     事务回滚，节点不建；
//   - 推进任务版本：新节点的清单从空变为全部 all_nodes 任务，按 bumpProbeVersion 的不变式必须推进。
func insertNode(tx *sql.Tx, name string, tokenHash []byte, createdAt int64, public bool) (int64, NewNodeTasks, error) {
	res, err := tx.Exec(`INSERT INTO node (name, token_hash, created_at, sort_order, public)
		SELECT ?, ?, ?, COALESCE(MAX(sort_order), -1) + 1, ? FROM node`, name, tokenHash, createdAt, public)
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
