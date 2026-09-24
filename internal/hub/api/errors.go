package api

import (
	"errors"
	"fmt"
	"math"

	"connectrpc.com/connect"
	"github.com/xjetry/probe/internal/hub/alert"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
)

// SQLite 的任务编号为有符号整数；拒绝不能表示的协议值，不能让它回绕成另一个编号。
func checkTaskID(id uint64, field string) error {
	if id > math.MaxInt64 {
		return invalid("%s: out of range", field)
	}
	return nil
}

// 校验方提供路径和约束，枚举词汇由双向转换共用的表裁定；包装错误不改变字段定位。
func fieldMessage(root string, field alert.FieldError) string {
	allowed := make([]string, len(field.Allowed))
	for i, v := range field.Allowed {
		switch {
		case root == "rule" && field.Path == "kind":
			allowed[i] = enumFor(alertKinds, store.AlertKind(v)).String()
		case root == "rule" && field.Path == "metric":
			allowed[i] = enumFor(probeMetrics, store.ProbeMetric(v)).String()
		case root == "channel" && field.Path == "kind":
			allowed[i] = enumFor(channelKinds, store.ChannelKind(v)).String()
		default:
			allowed[i] = v
		}
	}
	field.Allowed = allowed
	return root + "." + field.Path + " " + field.Detail()
}

// 存储层携带对象种类；保存与删除各自指定请求根路径，不从错误文本猜测对象。
func missingField(root, kind string) string {
	if root == "id" {
		return root
	}
	switch kind {
	case "node":
		if root == "task" {
			return "node_ids"
		}
		return root + ".node_ids"
	case "notify channel":
		if root == "rule" {
			return "rule.channel_ids"
		}
	case "probe task":
		if root == "rule" {
			return "rule.task_id"
		}
	}
	return root + ".id"
}

func (s *Service) operationError(err error, root, operation string) error {
	switch {
	case errors.Is(err, alert.ErrInvalid):
		var field alert.FieldError
		if errors.As(err, &field) {
			return connect.NewError(connect.CodeInvalidArgument, errors.New(fieldMessage(root, field)))
		}
	case errors.Is(err, probe.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrNotFound):
		var missing store.NotFoundError
		errors.As(err, &missing)
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: %w", missingField(root, missing.Kind), err))
	case errors.Is(err, store.ErrInUse):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", root, err))
	case errors.Is(err, store.ErrNodeLimit):
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s: %w", missingField(root, "node"), err))
	}
	s.log.Error(operation, "err", err)
	return internalError(operation)
}
