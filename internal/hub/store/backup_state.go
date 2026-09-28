package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *Store) RecordBackupSuccess(ctx context.Context, layer string) error {
	if layer != "config" && layer != "metrics" {
		return fmt.Errorf("unknown backup layer %q", layer)
	}
	return s.recordMaintenance(ctx, "backup_"+layer)
}

func (s *Store) BackupSuccessTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT name, finished_at FROM maintenance_state WHERE name IN ('backup_config', 'backup_metrics')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name string
		var at int64
		if err := rows.Scan(&name, &at); err != nil {
			return nil, err
		}
		out[name] = time.Unix(at, 0).UTC()
	}
	return out, rows.Err()
}

// 渠道与事件在同一写事务读取和提交；删除渠道也经写队列更新设置，因而不会落下悬空投递。
// 系统事件不属于节点或规则，不写 alert_state，避免与告警引擎的规则状态混用。
func (s *Store) RecordBackupEvent(ctx context.Context, transition Transition, summary string) (AlertEvent, error) {
	ev := AlertEvent{Transition: transition, Summary: summary, At: time.Unix(s.clk.Now().Unix(), 0).UTC()}
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, cfg, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		return recordAlertEvent(tx, &ev, cfg.Channels)
	})
	if err != nil {
		return AlertEvent{}, err
	}
	return ev, nil
}
