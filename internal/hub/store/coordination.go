package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OfflineGeneration 读离线变更代数（表的含义见 schema.go 的 hub_coordination）。缺行或负值返回错误：调用方据代数
// 判定"自上次确认以来有没有库外写入"，把读不出来当成没有变化，会让离线删除与换发永远不被重载。
func (s *Store) OfflineGeneration(ctx context.Context) (uint64, error) {
	var g int64
	err := s.r.QueryRowContext(ctx, "SELECT offline_generation FROM hub_coordination WHERE id = 1").Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("hub_coordination: row missing")
	}
	if err != nil {
		return 0, err
	}
	if g < 0 {
		return 0, fmt.Errorf("hub_coordination: offline_generation %d is negative", g)
	}
	return uint64(g), nil
}
