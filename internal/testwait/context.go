package testwait

import (
	"context"
	"sync"
)

// PauseContext 在首次读取 Done 时暂停，供测试在 database/sql 检查取消状态时
// 留住请求；entered 确认已抵达阻塞点，release 可重复调用以便失败路径清理。
// 仅给需要控制调用顺序的测试使用，不能作为实际请求的 context。
func PauseContext(parent context.Context) (ctx context.Context, entered <-chan struct{}, release func()) {
	c := &pausedContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{})}
	return c, c.entered, sync.OnceFunc(func() { close(c.release) })
}

type pausedContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *pausedContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.entered)
		<-c.release
	})
	return c.Context.Done()
}
