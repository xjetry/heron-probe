package store

import (
	"context"
	"database/sql"
	"sync"
)

// HoldWriterForTest 仅阻塞本实例的队列，不获取 SQLite 写锁，允许另一实例先提交撤销。
func (s *Store) HoldWriterForTest() (release func(), pending func() int, drain func() error) {
	entered, gate := make(chan struct{}), make(chan struct{})
	s.writeAsync(func(*sql.Tx) error { close(entered); <-gate; return nil }, nil)
	<-entered
	return sync.OnceFunc(func() { close(gate) }), func() int { return len(s.writes) }, func() error {
		return s.write(context.Background(), func(*sql.Tx) error { return nil })
	}
}
