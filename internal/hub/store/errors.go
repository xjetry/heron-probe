package store

import (
	"errors"
	"fmt"
)

var ErrNodeLimit = errors.New("node already has the maximum number of probe tasks")

// 错误由发现问题的存储层携带实体与约束；调用方不必解析文本即可保留哨兵分类。
type NotFoundError struct {
	Kind string
	ID   int64
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("%s %d does not exist", e.Kind, e.ID)
}

func (e NotFoundError) Is(target error) bool {
	return target == ErrNotFound
}

type NodeLimitError struct {
	NodeID int64
	Max    int
}

func (e NodeLimitError) Error() string {
	return fmt.Sprintf("node %d already has %d probe tasks (maximum %d)", e.NodeID, e.Max, e.Max)
}

func (e NodeLimitError) Is(target error) bool {
	return target == ErrNodeLimit
}
