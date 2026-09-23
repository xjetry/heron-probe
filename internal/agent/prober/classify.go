package prober

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// classify 统一连接与发送失败口径：这一次没有联通是可达性事实，计入丢包；本地无法发起才是 error。
func classify(err error) Outcome {
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return Outcome{Timeout: true}
	}
	for _, remote := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EHOSTDOWN} {
		if errors.Is(err, remote) {
			return Outcome{Timeout: true}
		}
	}
	return Outcome{Err: err.Error()}
}
