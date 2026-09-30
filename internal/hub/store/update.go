package store

import (
	"context"
	"database/sql"
	"errors"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// NodeUpdates 是运行簿记；恢复配置不应重放以前排队的安装授权。
func (s *Store) NodeUpdates(ctx context.Context) (map[int64]*heronv1.UpdateStatus, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT u.node_id,u.data FROM node_update u JOIN node n ON n.id=u.node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]*heronv1.UpdateStatus)
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		status := new(heronv1.UpdateStatus)
		if err := protojson.Unmarshal([]byte(data), status); err != nil {
			return nil, err
		}
		out[id] = status
	}
	return out, rows.Err()
}

func (s *Store) SaveNodeUpdate(ctx context.Context, id int64, status *heronv1.UpdateStatus) error {
	saved := proto.Clone(status).(*heronv1.UpdateStatus)
	err := s.write(ctx, func(tx *sql.Tx) error {
		var owner int64
		var oldData string
		if err := tx.QueryRow("SELECT owner_id,data FROM node_update WHERE node_id=?", id).Scan(&owner, &oldData); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		old := new(heronv1.UpdateStatus)
		if oldData != "" {
			if err := protojson.Unmarshal([]byte(oldData), old); err != nil {
				return err
			}
		}
		if old.GetTask().GetId() != saved.GetTask().GetId() {
			owner = OwnerID(ctx)
		}
		// 排队授权在真正下发的事务内再检查；已下发任务不能假称可以撤回。
		if owner != 0 && old.GetTask().GetState() == "queued" && saved.GetTask().GetState() == "dispatched" {
			t, err := scanAPIToken(tx.QueryRow(selectAPIToken+" WHERE id=?", owner).Scan)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) || !t.Allows(PermissionUpdate) || !t.AllowsNode(id) {
				saved.Task.State = "cancelled"
				saved.Task.Error = "API token authorization revoked before dispatch"
			}
		}
		data, err := protojson.Marshal(saved)
		if err != nil {
			return err
		}
		// 节点存在性在写事务中裁决，删除后晚到的上报不得重建簿记。
		res, err := tx.Exec("INSERT INTO node_update(node_id,data,owner_id) SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM node WHERE id=?) ON CONFLICT(node_id) DO UPDATE SET data=excluded.data,owner_id=excluded.owner_id", id, string(data), owner, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
	if err == nil {
		proto.Reset(status)
		proto.Merge(status, saved)
	}
	return err
}
