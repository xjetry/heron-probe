package store

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNodeLimit = errors.New("node already has the maximum number of probe tasks")

var ErrInUse = errors.New("in use")

type ObjectKind string

// 对象标识跨包消费：api 据此选择字段名，投递据此识别已消失的行。
// 统一引用常量，避免字符串字面量的拼写漂移绕过编译检查、令消费方静默失效。
const (
	ObjectNode          ObjectKind = "node"
	ObjectProbeTask     ObjectKind = "probe task"
	ObjectAlertRule     ObjectKind = "alert rule"
	ObjectNotifyChannel ObjectKind = "notify channel"
	ObjectAlertEvent    ObjectKind = "alert event"
	ObjectAlertDelivery ObjectKind = "alert delivery"
)

type RuleReference struct {
	ID   int64
	Name string
}

type InUseError struct {
	Kind  ObjectKind
	ID    int64
	Rules []RuleReference
}

func (e InUseError) Error() string {
	names := make([]string, len(e.Rules))
	for i, r := range e.Rules {
		names[i] = fmt.Sprintf("%s (id %d)", r.Name, r.ID)
	}
	return fmt.Sprintf("%s %d is referenced by alert rules: %s", e.Kind, e.ID, strings.Join(names, ", "))
}

func (e InUseError) Is(target error) bool { return target == ErrInUse }

// 错误由发现问题的存储层携带实体与约束；调用方不必解析文本即可保留哨兵分类。
type NotFoundError struct {
	Kind ObjectKind
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
