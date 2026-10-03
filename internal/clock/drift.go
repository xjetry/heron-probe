package clock

import (
	"time"

	"github.com/xjetry/heron-probe/internal/probelimit"
)

// MaxClockJump 是一轮上报循环内墙钟与单调钟增量的正常偏差上限，超出即"休眠信号"。
// 推导：
//   - 一轮的长度上限由 Runner 的有效间隔保证：hub 下发的间隔经 agentwire.ClampReportInterval
//     限定为 agentwire.MaxTTL/3=60s，失败退避封顶 3 倍间隔（client.Backoff）即 180s。
//   - 一轮内两钟的正常偏差来自 NTP slew，上限 500 ppm：180s 内不超过 90ms，是毫秒量级。
//   - 真实休眠至少数十秒：Linux 的 CLOCK_MONOTONIC 与 darwin 的 CLOCK_UPTIME_RAW 都不计休眠
//     时间，唤醒后 Δwall − Δmono ≈ 休眠时长。
//
// 20s 远大于毫秒级的正常偏差、小于真实休眠；同时必须小于 probelimit.MaxResultAge（120s）：
// 跨越休眠 S 的结果真龄比单调钟年龄大 S，S ≥ MaxResultAge 时它们必然超龄应弃，而阈值更小
// 保证这类休眠必然被判出、在下一轮循环开头（先于取队列上报）整体作废，不会当作合法结果上报。
// NTP 步进也会越过阈值：与休眠同处理，代价是一个周期无速率与至多 MaxResultAge 的积压结果被弃，
// 不为区分两者再加判据。
const MaxClockJump = 20 * time.Second

// 休眠信号必须覆盖所有"结果必然超龄"的休眠：MaxResultAge ≤ MaxClockJump 时存在
// 无法判出却已使全部积压结果超龄的休眠区间。
const _ = uint(probelimit.MaxResultAge - MaxClockJump - 1)

// Drift 逐轮比较墙钟与单调钟的增量，识别单调钟没有经历的墙钟跳跃（休眠、NTP 步进）。
// 首次 Observe 只建立基线，不报跳变。
type Drift struct {
	wall  time.Time
	mono  time.Duration
	began bool
}

// Observe 记录一对读数，返回与上一轮的增量；jumped 表示 |Δwall − Δmono| 越过 MaxClockJump。
// 取绝对值是因为墙钟被向后拨同样是单调钟未经历的跳变。
// now 与 mono 必须是同一时刻紧邻读取的一对值（Runner 在循环开头连续取），
// 否则两次读取之间的流逝只计入 Δmono，被误算成跳变的一部分。
func (d *Drift) Observe(now time.Time, mono time.Duration) (wallDelta, monoDelta time.Duration, jumped bool) {
	if !d.began {
		d.began, d.wall, d.mono = true, now, mono
		return 0, 0, false
	}
	wallDelta, monoDelta = now.Sub(d.wall), mono-d.mono
	d.wall, d.mono = now, mono
	gap := wallDelta - monoDelta
	if gap < 0 {
		gap = -gap
	}
	return wallDelta, monoDelta, gap > MaxClockJump
}
