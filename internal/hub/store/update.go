package store

import (
	"context"
	"database/sql"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/encoding/protojson"
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
	data, err := protojson.Marshal(status)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		// 节点存在性在写事务中裁决，删除后晚到的上报不得重建簿记。
		res, err := tx.Exec("INSERT INTO node_update(node_id,data) SELECT ?,? WHERE EXISTS (SELECT 1 FROM node WHERE id=?) ON CONFLICT(node_id) DO UPDATE SET data=excluded.data", id, string(data), id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
}
