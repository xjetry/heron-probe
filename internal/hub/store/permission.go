package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

type Permission string

const (
	PermissionConfigure Permission = "configure"
	PermissionCreate    Permission = "create"
	PermissionRegister  Permission = "register"
	PermissionRotate    Permission = "rotate"
	PermissionDelete    Permission = "delete"
	PermissionUpdate    Permission = "update"
)

var ErrPermission = errors.New("operation or resource is outside the API token grant")

type TokenGrant struct {
	Permissions []Permission
	AllNodes    bool
	NodeIDs     []int64
}

func (g TokenGrant) Allows(p Permission) bool { return slices.Contains(g.Permissions, p) }
func (g TokenGrant) AllowsNode(id int64) bool {
	return id > 0 && (g.AllNodes || slices.Contains(g.NodeIDs, id))
}
func (g TokenGrant) AllowsSelector(all bool, tags []string, ids []int64) bool {
	if g.AllNodes {
		return true
	}
	// 空显式集合没有归属，不能据此认领其他主体留下的共享对象。
	if all || len(tags) != 0 || len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !g.AllowsNode(id) {
			return false
		}
	}
	return true
}

func (g TokenGrant) validate(tx *sql.Tx) error {
	for _, p := range g.Permissions {
		switch p {
		case PermissionConfigure, PermissionCreate, PermissionRegister, PermissionRotate, PermissionDelete, PermissionUpdate:
		default:
			return ErrPermission
		}
	}
	if g.AllNodes && len(g.NodeIDs) != 0 {
		return ErrPermission
	}
	for _, id := range g.NodeIDs {
		ok, err := nodeExistsTx(tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, token APIToken) context.Context {
	return context.WithValue(ctx, principalKey{}, token)
}
func Principal(ctx context.Context) (APIToken, bool) {
	t, ok := ctx.Value(principalKey{}).(APIToken)
	return t, ok
}
func OwnerID(ctx context.Context) int64 { t, _ := Principal(ctx); return t.ID }

// 范围从数据库关系表读取，建节点后同一请求的回读也能看见刚获得的授权；空列表不放宽。
func nodeScopeSQL(ctx context.Context, column string) string {
	t, ok := Principal(ctx)
	if !ok {
		return "1"
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM api_token a WHERE a.id=%d AND (a.all_nodes=1 OR EXISTS (SELECT 1 FROM api_token_node g WHERE g.token_id=a.id AND g.node_id=%s)))", t.ID, column)
}

func grantNode(tx *sql.Tx, owner, node int64) error {
	if owner == 0 {
		return nil
	}
	_, err := tx.Exec("INSERT OR IGNORE INTO api_token_node (token_id, node_id) SELECT ?, ? WHERE EXISTS (SELECT 1 FROM api_token WHERE id=? AND all_nodes=0)", owner, node, owner)
	return err
}
