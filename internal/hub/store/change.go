package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrPreview   = errors.New("change preview rolled back")
	ErrReplay    = errors.New("change already committed")
	ErrConflict  = errors.New("resource changed; preview again")
	ErrRequestID = errors.New("request_id was used for a different change")
)

type Operation struct {
	ID                             string
	OwnerID                        int64
	RequestID, RequestHash, Action string
	ResourceID                     int64
	BeforeJSON, AfterJSON          string
	CommittedAt                    int64
}

// ChangeKind 是变更目标的资源种类。同种资源共用快照列白名单、凭据摘要与 ResourceID 的分配来源（changeKinds）；
// String 的取值进入版本摘要与快照的主行键，改名会让进行中的预览全部失配、审计里的 JSON 键漂移。
type ChangeKind uint8

const (
	ChangeNode ChangeKind = iota + 1
	ChangeProbe
	ChangeAlert
	ChangeWindow
	ChangeUpdate
	ChangeTag
)

func (k ChangeKind) String() string {
	switch k {
	case ChangeNode:
		return "node"
	case ChangeProbe:
		return "probe"
	case ChangeAlert:
		return "alert"
	case ChangeWindow:
		return "window"
	case ChangeUpdate:
		return "update"
	case ChangeTag:
		return "tag"
	}
	return fmt.Sprintf("ChangeKind(%d)", uint8(k))
}

// ChangeAction 是一次类型化变更的操作，与 ExecuteChangeRequest.change 的 oneof 分支一一对应。零值非法：
// 未登记的操作在 WithChange 处 panic、在 ChangeVersion 处报错，不会落到某个默认分支被当作合法操作执行。
type ChangeAction uint8

const (
	ActionCreateNode ChangeAction = iota + 1
	ActionUpdateNode
	ActionDeleteNode
	ActionRotateNodeToken
	ActionOpenRegisterWindow
	ActionCloseRegisterWindow
	ActionSaveProbeTask
	ActionDeleteProbeTask
	ActionSaveAlertRule
	ActionDeleteAlertRule
	ActionStartUpdate
	ActionCancelUpdate
	ActionDeleteTag
)

// String 是 operation.action 列与回执里的动作名：已持久化的回执按它展示，改名会让新旧回执的同一操作名字不同。
func (a ChangeAction) String() string {
	if spec, ok := changeActions[a]; ok {
		return spec.name
	}
	return fmt.Sprintf("ChangeAction(%d)", uint8(a))
}

// Kind 与 Permission 都由登记表推出，Change 不另存一份，二者无法与操作失配。未登记的操作返回零值。
func (a ChangeAction) Kind() ChangeKind { return changeActions[a].kind }

func (a ChangeAction) Permission() Permission { return changeActions[a].permission }

type changeActionSpec struct {
	name       string
	kind       ChangeKind
	permission Permission
	// creates 的操作在写之前没有资源身份：ResourceID 必须为 0，写后由种类的 sequence 填入。
	creates bool
	// authorizeAfterWrite 的操作在写后、ResourceID 已知时再做一次资源授权：保存可能新建资源或改变它的作用域，
	// 写前只能裁决旧状态与请求里的选择器。
	authorizeAfterWrite bool
}

// changeActions 是类型化变更的唯一登记表：changeWrite、snapshot、ChangeVersion 与 api 的 prepareChange 都只从这里取
// 操作的种类、权限与写后授权，新增 oneof 分支必须在这里登记，否则 WithChange 拒绝它。
var changeActions = map[ChangeAction]changeActionSpec{
	ActionCreateNode:          {name: "create_node", kind: ChangeNode, permission: PermissionCreate, creates: true},
	ActionUpdateNode:          {name: "update_node", kind: ChangeNode, permission: PermissionConfigure},
	ActionDeleteNode:          {name: "delete_node", kind: ChangeNode, permission: PermissionDelete},
	ActionRotateNodeToken:     {name: "rotate_node_token", kind: ChangeNode, permission: PermissionRotate},
	ActionOpenRegisterWindow:  {name: "open_register_window", kind: ChangeWindow, permission: PermissionRegister},
	ActionCloseRegisterWindow: {name: "close_register_window", kind: ChangeWindow, permission: PermissionRegister},
	ActionSaveProbeTask:       {name: "save_probe_task", kind: ChangeProbe, permission: PermissionConfigure, authorizeAfterWrite: true},
	ActionDeleteProbeTask:     {name: "delete_probe_task", kind: ChangeProbe, permission: PermissionConfigure, authorizeAfterWrite: true},
	ActionSaveAlertRule:       {name: "save_alert_rule", kind: ChangeAlert, permission: PermissionConfigure, authorizeAfterWrite: true},
	ActionDeleteAlertRule:     {name: "delete_alert_rule", kind: ChangeAlert, permission: PermissionConfigure, authorizeAfterWrite: true},
	ActionStartUpdate:         {name: "start_update", kind: ChangeUpdate, permission: PermissionUpdate},
	ActionCancelUpdate:        {name: "cancel_update", kind: ChangeUpdate, permission: PermissionUpdate},
	ActionDeleteTag:           {name: "delete_tag", kind: ChangeTag, permission: PermissionConfigure},
}

type snapshotQuery struct{ name, sql string }

type changeKindSpec struct {
	// row 读资源主行，快照里以种类名为键；relations 读从属关系，各以自己的名字为键。
	row       string
	relations []snapshotQuery
	// secret 读凭据哈希，只进入版本摘要、不进快照：换发凭据必须改变版本，审计与预览不能带出哈希。
	secret string
	// sequence 读 AUTOINCREMENT 刚分配的身份；为空的种类没有由写分配的资源身份。
	sequence string
	// scope 读带节点选择器的资源（探测任务、告警规则）的作用域，供 RuleScope 使用；其它种类为 nil。
	scope *ruleScopeQueries
}

// ruleScopeQueries 的 head 读 (all_nodes, 引用的探测任务 id)，tags 计选择器标签数，nodes 列显式节点。
type ruleScopeQueries struct{ head, tags, nodes string }

// 白名单列同时决定预览和审计，不从原始请求或响应复制内容；凭据哈希只进入版本摘要。
var changeKinds = map[ChangeKind]changeKindSpec{
	ChangeNode: {
		row:       `SELECT id,name,public,note,traffic_reset_day,offline_grace_s,price,currency,billing_cycle,expires_on,auto_renew,country_pin,maintenance,public_remark,traffic_quota_bytes,traffic_quota_mode FROM node WHERE id=?`,
		relations: []snapshotQuery{{"tags", `SELECT t.name FROM node_tag n JOIN tag t ON t.id=n.tag_id WHERE n.node_id=? ORDER BY t.name_fold`}},
		secret:    "SELECT hex(token_hash) FROM node WHERE id=?",
		sequence:  "SELECT seq FROM sqlite_sequence WHERE name='node'",
	},
	ChangeProbe: {
		row:       "SELECT id,kind,target,interval_s,timeout_ms,created_at,all_nodes,sort_order,dns_server,cert_spki_sha256,config_id FROM probe_task WHERE id=?",
		relations: []snapshotQuery{{"nodes", "SELECT node_id FROM probe_task_node WHERE task_id=? ORDER BY node_id"}, {"tags", "SELECT tag_id FROM probe_task_tag WHERE task_id=? ORDER BY tag_id"}},
		sequence:  "SELECT seq FROM sqlite_sequence WHERE name='probe_task'",
		scope: &ruleScopeQueries{
			head:  "SELECT all_nodes, 0 FROM probe_task WHERE id = ?",
			tags:  "SELECT COUNT(*) FROM probe_task_tag WHERE task_id = ?",
			nodes: "SELECT node_id FROM probe_task_node WHERE task_id = ?",
		},
	},
	ChangeAlert: {
		row:       "SELECT id,name,kind,enabled,all_nodes,task_id,metric,threshold,for_minutes,created_at,days_before,resource_metric,recovery_threshold FROM alert_rule WHERE id=?",
		relations: []snapshotQuery{{"nodes", "SELECT node_id FROM alert_rule_node WHERE rule_id=? ORDER BY node_id"}, {"tags", "SELECT tag_id FROM alert_rule_tag WHERE rule_id=? ORDER BY tag_id"}, {"channels", "SELECT channel_id FROM alert_rule_channel WHERE rule_id=? ORDER BY channel_id"}},
		sequence:  "SELECT seq FROM sqlite_sequence WHERE name='alert_rule'",
		scope: &ruleScopeQueries{
			head:  "SELECT all_nodes, COALESCE(task_id, 0) FROM alert_rule WHERE id = ?",
			tags:  "SELECT COUNT(*) FROM alert_rule_tag WHERE rule_id = ?",
			nodes: "SELECT node_id FROM alert_rule_node WHERE rule_id = ?",
		},
	},
	ChangeWindow: {
		row:    "SELECT owner_id,expires_at,remaining FROM register_window WHERE owner_id=?",
		secret: "SELECT hex(key_hash) FROM register_window WHERE owner_id=?",
	},
	ChangeUpdate: {
		row: "SELECT node_id,data FROM node_update WHERE node_id=?",
	},
	ChangeTag: {
		row:       "SELECT id,name FROM tag WHERE id=?",
		relations: []snapshotQuery{{"nodes", "SELECT node_id FROM node_tag WHERE tag_id=? ORDER BY node_id"}},
	},
}

var errUnregisteredAction = errors.New("change action is not registered")

// spec 取操作与它所属种类的登记；两张表任一缺项都是装配错误。
func (c *Change) spec() (changeActionSpec, changeKindSpec, error) {
	action, ok := changeActions[c.Action]
	if !ok {
		return changeActionSpec{}, changeKindSpec{}, fmt.Errorf("%w: %s", errUnregisteredAction, c.Action)
	}
	kind, ok := changeKinds[action.kind]
	if !ok {
		return changeActionSpec{}, changeKindSpec{}, fmt.Errorf("%w: %s has no kind %s", errUnregisteredAction, c.Action, action.kind)
	}
	return action, kind, nil
}

// Change 携带一次类型化变更穿过 handler 到达 store 的写边界。Action 是操作身份；内嵌的 Operation.Action 是
// 回执里持久化的动作名，由写边界按 Action.String() 填写，调用方不设置它。
type Change struct {
	Operation
	Action ChangeAction
	// Policy 裁决这次变更是否被允许；store 只按固定次序调用它，自己不作允许或拒绝的决定。
	Policy          ChangePolicy
	Preview         bool
	ExpectedVersion string
	Version         string
	Err             error
	completed       bool
	Selector        *NodeSelector
	ReferenceTask   int64
}

// ChangeReader 是策略在写事务内（或 ChangeVersion 的读事务内）可做的全部读取。策略只经它读库，裁决与写
// 看到的是同一份快照；接口只列策略需要的读，不暴露事务本身。
type ChangeReader interface {
	// APIToken 读 token 的当前行；不存在时返回 ErrNotFound。
	APIToken(id int64) (APIToken, error)
	// RuleScope 读探测任务或告警规则的当前作用域；不存在时返回 ErrNotFound。
	RuleScope(kind ChangeKind, id int64) (RuleScope, error)
}

// RuleScope 是带节点选择器的资源的作用域。TaskID 是告警规则引用的探测任务（没有引用或资源是探测任务时为 0）。
type RuleScope struct {
	AllNodes bool
	TagCount int
	NodeIDs  []int64
	TaskID   int64
}

// ChangePolicy 是类型化变更的授权策略。changeWrite 的调用次序固定为 主体 → 重放 → 作用域 → 写 → 资源：
//   - Principal 鉴别主体并核对操作所需的权限位，会话主体返回 nil token；
//   - 重放检查由 store 在 Principal 之后、Scope 之前完成，已提交的请求在作用域被收回后仍能取回回执；
//   - Scope 按当前库状态裁决作用域，ChangeVersion 与写前共用它，预览与执行的准入口径因此相同；
//   - Resource 只对登记了 authorizeAfterWrite 的操作在写之后调用，此时 ResourceID 已知、资源已是写后状态。
//
// 拒绝的错误契约写在 permission.go 的权限哨兵上；写边界把拒绝记进 Change.Err，其它错误按普通写失败处理。
type ChangePolicy interface {
	Principal(ctx context.Context, r ChangeReader, c *Change) (*APIToken, error)
	Scope(ctx context.Context, r ChangeReader, c *Change, token *APIToken) error
	Resource(ctx context.Context, r ChangeReader, c *Change, token *APIToken) error
}

// txReader 在调用方的事务上实现 ChangeReader。
type txReader struct{ tx *sql.Tx }

func (r txReader) APIToken(id int64) (APIToken, error) {
	t, err := scanAPIToken(r.tx.QueryRow(selectAPIToken+" WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	return t, err
}

func (r txReader) RuleScope(kind ChangeKind, id int64) (RuleScope, error) {
	q := changeKinds[kind].scope
	if q == nil {
		return RuleScope{}, fmt.Errorf("change kind %s has no rule scope", kind)
	}
	var out RuleScope
	err := r.tx.QueryRow(q.head, id).Scan(&out.AllNodes, &out.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return RuleScope{}, ErrNotFound
	}
	if err != nil {
		return RuleScope{}, err
	}
	if err := r.tx.QueryRow(q.tags, id).Scan(&out.TagCount); err != nil {
		return RuleScope{}, err
	}
	out.NodeIDs, err = scanIDs(r.tx.Query(q.nodes, id))
	return out, err
}

type changeKey struct{}

// WithChange 把 c 装进 ctx；没有策略或操作未登记都是装配错误，在这里 panic 而不是等到写边界才发现。
func WithChange(ctx context.Context, c *Change) context.Context {
	if c.Policy == nil {
		panic("store.WithChange: Change.Policy is nil")
	}
	if _, _, err := c.spec(); err != nil {
		panic("store.WithChange: " + err.Error())
	}
	return context.WithValue(ctx, changeKey{}, c)
}

// changeWrite 包住一个业务提交，不包住提交后的后台派生工作。错误（含预览、重放）仍按
// write 的契约表示本次事务没有应用，auth、探测和告警缓存因此不会发布回滚后的状态。
func (s *Store) changeWrite(ctx context.Context, c *Change, fn func(*sql.Tx) error) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		action, kind, err := c.spec()
		if err != nil {
			return err
		}
		r := txReader{tx}
		token, err := c.Policy.Principal(ctx, r, c)
		if err != nil {
			c.Err = err
			return err
		}
		if !c.Preview {
			op, err := readOperation(tx, operationOwner(ctx), c.RequestID)
			if err == nil {
				if op.RequestHash != c.RequestHash {
					c.Err = ErrRequestID
				} else {
					c.Operation = op
					c.Err = ErrReplay
				}
				return c.Err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := c.Policy.Scope(ctx, r, c, token); err != nil {
			c.Err = err
			return err
		}
		c.Operation.Action = c.Action.String()
		before, version, err := c.snapshot(tx)
		if err != nil {
			return err
		}
		c.Version = version
		if c.ExpectedVersion != version {
			c.Err = ErrConflict
			return c.Err
		}
		c.BeforeJSON = before
		if err := fn(tx); err != nil {
			return err
		}
		// ResourceID 为 0 的成功写只能是建资源（建节点，或不带 id 的保存探测任务、告警规则）：身份在写里才分配。
		if c.ResourceID == 0 && kind.sequence != "" {
			if err := tx.QueryRow(kind.sequence).Scan(&c.ResourceID); err != nil {
				return err
			}
		}
		if action.authorizeAfterWrite {
			if err := c.Policy.Resource(ctx, r, c, token); err != nil {
				c.Err = err
				return err
			}
		}
		c.AfterJSON, _, err = c.snapshot(tx)
		if err != nil {
			return err
		}
		if c.Preview {
			c.Err = ErrPreview
			return c.Err
		}
		c.CommittedAt = s.clk.Now().Unix()
		c.ID = operationID(operationOwner(ctx), c.RequestID)
		// 详细差异保留 90 天，幂等墓碑永久保留，清理审计不能让旧 request_id 再次执行。
		if _, err := tx.Exec("UPDATE operation SET before_json='',after_json='' WHERE committed_at < ? AND (before_json!='' OR after_json!='')", c.CommittedAt-90*24*60*60); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO operation (owner_key,owner_id,request_id,request_hash,action,resource_id,before_json,after_json,committed_at) VALUES (?,?,?,?,?,?,?,?,?)`, operationOwner(ctx), c.OwnerID, c.RequestID, c.RequestHash, c.Operation.Action, c.ResourceID, c.BeforeJSON, c.AfterJSON, c.CommittedAt)
		return err
	}
}

func (c *Change) snapshot(tx *sql.Tx) (string, string, error) {
	action, spec, err := c.spec()
	if err != nil {
		return "", "", err
	}
	queries := append([]snapshotQuery{{action.kind.String(), spec.row}}, spec.relations...)
	data := map[string]any{}
	for _, q := range queries {
		rows, err := tx.Query(q.sql, c.ResourceID)
		if err != nil {
			return "", "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", "", err
		}
		list := make([]map[string]any, 0)
		for rows.Next() {
			values, args := make([]any, len(columns)), make([]any, len(columns))
			for i := range args {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				return "", "", err
			}
			row := map[string]any{}
			for i, name := range columns {
				row[name] = values[i]
			}
			list = append(list, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return "", "", err
		}
		data[q.name] = list
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "", "", err
	}
	var secret string
	if spec.secret != "" {
		if err := tx.QueryRow(spec.secret, c.ResourceID).Scan(&secret); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", action.kind, c.ResourceID, b, secret)))
	return string(b), hex.EncodeToString(h[:]), nil
}

func (s *Store) ChangeVersion(ctx context.Context, c *Change) (string, error) {
	if _, _, err := c.spec(); err != nil {
		return "", err
	}
	if c.Policy == nil {
		panic("store.ChangeVersion: Change.Policy is nil")
	}
	tx, err := s.r.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	r := txReader{tx}
	token, err := c.Policy.Principal(ctx, r, c)
	if err != nil {
		return "", err
	}
	if err := c.Policy.Scope(ctx, r, c, token); err != nil {
		return "", err
	}
	_, version, err := c.snapshot(tx)
	return version, err
}

const selectOperation = "SELECT owner_key,owner_id,request_id,request_hash,action,resource_id,before_json,after_json,committed_at FROM operation"

// 回执身份取自数据库主键而非可复用的数字 token ID；摘要不向 API 消费者暴露凭据哈希。
func operationID(owner, request string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(owner+"\x00"+request)))
}

func scanOperation(scan func(...any) error) (Operation, error) {
	var o Operation
	var owner string
	err := scan(&owner, &o.OwnerID, &o.RequestID, &o.RequestHash, &o.Action, &o.ResourceID, &o.BeforeJSON, &o.AfterJSON, &o.CommittedAt)
	if err == nil {
		o.ID = operationID(owner, o.RequestID)
	}
	return o, err
}
func operationOwner(ctx context.Context) string {
	if p, ok := Principal(ctx); ok {
		return p.Identity
	}
	return "session"
}
func readOperation(tx *sql.Tx, owner string, id string) (Operation, error) {
	return scanOperation(tx.QueryRow(selectOperation+" WHERE owner_key=? AND request_id=?", owner, id).Scan)
}
func (s *Store) FindOperation(ctx context.Context, owner int64, id string) (Operation, error) {
	o, err := scanOperation(s.r.QueryRowContext(ctx, selectOperation+" WHERE owner_id=? AND owner_key=? AND request_id=?", owner, operationOwner(ctx), id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return o, err
}
func (s *Store) ListOperations(ctx context.Context, owner int64, id string, limit int) ([]Operation, error) {
	query, args := selectOperation+" WHERE owner_id=?", []any{owner}
	if p, ok := Principal(ctx); ok {
		query += " AND owner_key=?"
		args = append(args, p.Identity)
	}
	if id != "" {
		query += " AND request_id=?"
		args = append(args, id)
	}
	query += " ORDER BY committed_at DESC,request_id LIMIT ?"
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		o, err := scanOperation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
