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

type Change struct {
	Operation
	Kind            string
	Permission      Permission
	Preview         bool
	ExpectedVersion string
	Version         string
	Err             error
	completed       bool
	Selector        *NodeSelector
	ReferenceTask   int64
}

type changeKey struct{}

func WithChange(ctx context.Context, c *Change) context.Context {
	return context.WithValue(ctx, changeKey{}, c)
}

// changeWrite 包住一个业务提交，不包住提交后的后台派生工作。错误（含预览、重放）仍按
// write 的契约表示本次事务没有应用，auth、探测和告警缓存因此不会发布回滚后的状态。
func (s *Store) changeWrite(ctx context.Context, c *Change, fn func(*sql.Tx) error) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		token, err := c.authorizePrincipal(ctx, tx)
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
		if err := c.authorizeScope(tx, token); err != nil {
			c.Err = err
			return err
		}
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
		if c.ResourceID == 0 && (c.Kind == "node" || c.Kind == "probe" || c.Kind == "alert") {
			table := map[string]string{"node": "node", "probe": "probe_task", "alert": "alert_rule"}[c.Kind]
			if err := tx.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = ?", table).Scan(&c.ResourceID); err != nil {
				return err
			}
		}
		if c.Kind == "probe" || c.Kind == "alert" {
			if err := c.authorize(ctx, tx); err != nil {
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
		_, err = tx.Exec(`INSERT INTO operation (owner_key,owner_id,request_id,request_hash,action,resource_id,before_json,after_json,committed_at) VALUES (?,?,?,?,?,?,?,?,?)`, operationOwner(ctx), c.OwnerID, c.RequestID, c.RequestHash, c.Action, c.ResourceID, c.BeforeJSON, c.AfterJSON, c.CommittedAt)
		return err
	}
}

func (c *Change) authorize(ctx context.Context, tx *sql.Tx) error {
	t, err := c.authorizePrincipal(ctx, tx)
	if err != nil {
		return err
	}
	return c.authorizeScope(tx, t)
}

func (c *Change) authorizePrincipal(ctx context.Context, tx *sql.Tx) (*APIToken, error) {
	p, bearer := Principal(ctx)
	if !bearer {
		return nil, nil
	}
	t, err := scanAPIToken(tx.QueryRow(selectAPIToken+" WHERE id = ?", p.ID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPermission
	}
	if err != nil {
		return nil, err
	}
	if t.Identity != p.Identity || !t.Allows(c.Permission) {
		return nil, ErrPermission
	}
	return &t, nil
}

func (c *Change) authorizeScope(tx *sql.Tx, t *APIToken) error {
	if t == nil {
		return nil
	}
	if c.Selector != nil && !t.AllowsSelector(c.Selector.AllNodes, c.Selector.Tags, c.Selector.NodeIDs) {
		return ErrPermission
	}
	if c.ReferenceTask != 0 {
		if err := authorizeRule(tx, t.TokenGrant, "probe", c.ReferenceTask); err != nil {
			return err
		}
	}
	switch c.Kind {
	case "node":
		if c.Permission == PermissionCreate {
			return nil
		}
		if !t.AllowsNode(c.ResourceID) {
			return ErrPermission
		}
	case "update":
		if !t.AllowsNode(c.ResourceID) {
			return ErrPermission
		}
	case "window":
		if c.ResourceID != t.ID {
			return ErrPermission
		}
	case "tag":
		if !t.AllNodes {
			return ErrPermission
		}
	case "probe", "alert":
		if c.ResourceID == 0 {
			return nil
		}
		return authorizeRule(tx, t.TokenGrant, c.Kind, c.ResourceID)
	default:
		return ErrPermission
	}
	return nil
}

func authorizeRule(tx *sql.Tx, g TokenGrant, kind string, id int64) error {
	if g.AllNodes {
		return nil
	}
	table, key := "probe_task", "task_id"
	if kind == "alert" {
		table, key = "alert_rule", "rule_id"
	}
	var all bool
	err := tx.QueryRow("SELECT all_nodes FROM "+table+" WHERE id = ?", id).Scan(&all)
	// 删除后的空资源可通过；删除前的检查已完成，未找到不等于认领空作用域。
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var tags int
	if err := tx.QueryRow("SELECT COUNT(*) FROM "+table+"_tag WHERE "+key+" = ?", id).Scan(&tags); err != nil {
		return err
	}
	ids, err := scanIDs(tx.Query("SELECT node_id FROM "+table+"_node WHERE "+key+" = ?", id))
	if err != nil {
		return err
	}
	if all || tags != 0 || !g.AllowsSelector(false, nil, ids) {
		return ErrPermission
	}
	if kind == "alert" {
		var task sql.NullInt64
		if err := tx.QueryRow("SELECT task_id FROM alert_rule WHERE id = ?", id).Scan(&task); err != nil {
			return err
		}
		if task.Valid && task.Int64 != 0 {
			return authorizeRule(tx, g, "probe", task.Int64)
		}
	}
	return nil
}

type snapshotQuery struct{ name, sql string }

// 白名单列同时决定预览和审计，不从原始请求或响应复制内容；凭据哈希只进入版本摘要。
func (c *Change) snapshot(tx *sql.Tx) (string, string, error) {
	var queries []snapshotQuery
	var secretQuery string
	switch c.Kind {
	case "node":
		queries = []snapshotQuery{{"node", `SELECT id,name,public,note,traffic_reset_day,offline_grace_s,price,currency,billing_cycle,expires_on,auto_renew,country_pin FROM node WHERE id=?`},
			{"tags", `SELECT t.name FROM node_tag n JOIN tag t ON t.id=n.tag_id WHERE n.node_id=? ORDER BY t.name_fold`}}
		secretQuery = "SELECT hex(token_hash) FROM node WHERE id=?"
	case "probe":
		queries = []snapshotQuery{{"probe", "SELECT id,kind,target,interval_s,timeout_ms,created_at,all_nodes,sort_order FROM probe_task WHERE id=?"}, {"nodes", "SELECT node_id FROM probe_task_node WHERE task_id=? ORDER BY node_id"}, {"tags", "SELECT tag_id FROM probe_task_tag WHERE task_id=? ORDER BY tag_id"}}
	case "alert":
		queries = []snapshotQuery{{"alert", "SELECT id,name,kind,enabled,all_nodes,task_id,metric,threshold,for_minutes,created_at,days_before,resource_metric,recovery_threshold FROM alert_rule WHERE id=?"}, {"nodes", "SELECT node_id FROM alert_rule_node WHERE rule_id=? ORDER BY node_id"}, {"tags", "SELECT tag_id FROM alert_rule_tag WHERE rule_id=? ORDER BY tag_id"}, {"channels", "SELECT channel_id FROM alert_rule_channel WHERE rule_id=? ORDER BY channel_id"}}
	case "window":
		queries = []snapshotQuery{{"window", "SELECT owner_id,expires_at,remaining FROM register_window WHERE owner_id=?"}}
		secretQuery = "SELECT hex(key_hash) FROM register_window WHERE owner_id=?"
	case "update":
		queries = []snapshotQuery{{"update", "SELECT node_id,data FROM node_update WHERE node_id=?"}}
	case "tag":
		queries = []snapshotQuery{{"tag", "SELECT id,name FROM tag WHERE id=?"}, {"nodes", "SELECT node_id FROM node_tag WHERE tag_id=? ORDER BY node_id"}}
	default:
		return "", "", ErrPermission
	}
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
	if secretQuery != "" {
		if err := tx.QueryRow(secretQuery, c.ResourceID).Scan(&secret); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", c.Kind, c.ResourceID, b, secret)))
	return string(b), hex.EncodeToString(h[:]), nil
}

func (s *Store) ChangeVersion(ctx context.Context, c *Change) (string, error) {
	tx, err := s.r.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := c.authorize(ctx, tx); err != nil {
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
