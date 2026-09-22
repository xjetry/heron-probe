package ingest

import (
	"context"
	"time"

	"github.com/xjetry/probe/internal/hub/metric"
)

// maxPendingBatches 限制待重试列表：数据库长时间不可用时内存不无限增长，
// 满了丢最旧的一批并记日志。
const maxPendingBatches = 64

// Flush 把已闭合的分钟桶（all 为 true 时包括仍开着的）交给写协程。
// 写失败的批次留在待重试列表，下次先重试它们；被写协程按水位拒绝的行
// 不算失败——它们已经不可能再被正确并入。
func (s *Service) Flush(ctx context.Context, all bool) {
	var rows []metric.Row
	if all {
		rows = s.live.Drain()
	} else {
		rows = s.live.Flush()
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(rows) > 0 {
		s.pending = append(s.pending, rows)
	}
	for len(s.pending) > maxPendingBatches {
		s.log.Error("dropping oldest unflushed minute batch", "rows", len(s.pending[0]))
		s.pending = s.pending[1:]
	}
	for len(s.pending) > 0 {
		batch := s.pending[0]
		rejected, err := s.writer.WriteMinuteRows(ctx, batch)
		if err != nil {
			s.log.Error("minute flush failed, keeping batch for retry", "err", err, "batches", len(s.pending))
			return
		}
		if rejected > 0 {
			s.log.Warn("minute rows dropped at rollup watermark", "rejected", rejected)
		}
		s.pending = s.pending[1:]
	}
}

// RunFlusher 在每个分钟边界后半秒刷出一次，ctx 结束时把全部桶刷出后返回。
// 半秒的偏移保证按墙钟分钟闭合的桶在刷出时确实已经闭合。
func (s *Service) RunFlusher(ctx context.Context) {
	for {
		wall := s.clk.Now()
		next := wall.Truncate(time.Minute).Add(time.Minute + 500*time.Millisecond)
		timer := time.NewTimer(next.Sub(wall))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.Flush(context.Background(), true)
			return
		case <-timer.C:
			s.Flush(ctx, false)
		}
	}
}
