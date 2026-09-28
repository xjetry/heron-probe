package store

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNodeLimit 是每节点任务上限的分类哨兵：保存侧的 NodeLimitError 与建节点侧的 InheritedLimitError 都经 Is 归入它，
// 超限的是哪个节点、多少个任务由这两个类型的原文给出。哨兵文案只命名类别，不写成"某节点已满"：保存侧在写入之后
// 计数，拒绝的是会超出上限的写入，而不是已满的节点；建节点侧被拒的节点随事务回滚，并不存在。
var ErrNodeLimit = errors.New("probe task limit per node exceeded")

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

// NodeLimitError 是保存任务之后某个现有节点的任务数（显式分配加全部 all_nodes 任务）超过上限。
type NodeLimitError struct {
	NodeID int64
	Tasks  int
	Max    int
}

func (e NodeLimitError) Error() string {
	return fmt.Sprintf("node %d would have %d probe tasks (maximum %d)", e.NodeID, e.Tasks, e.Max)
}

func (e NodeLimitError) Is(target error) bool {
	return target == ErrNodeLimit
}

// InheritedLimitError 是建节点时新节点会继承的 all_nodes 任务数超过上限：新节点没有显式分配，它的任务数就是
// all_nodes 任务的个数。
type InheritedLimitError struct {
	Tasks int
	Max   int
}

func (e InheritedLimitError) Error() string {
	return fmt.Sprintf("a new node would inherit %d all-nodes probe tasks (maximum %d per node); assign some of them to explicit nodes or delete them first", e.Tasks, e.Max)
}

func (e InheritedLimitError) Is(target error) bool {
	return target == ErrNodeLimit
}
