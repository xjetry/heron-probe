// Package metric 是指标列的唯一描述处。
//
// 不变式：建表语句、内存桶的折叠、写库时的加法合并、查询与上卷的 SQL 都
// 从 Columns 生成；不存在需要手工保持一致的第二份字段清单。新增一个指标
// 是描述表加一项加一次迁移，而不是在四个地方各改一行。
package metric

import (
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
)

type Kind uint8

const (
	// Mean 存 sum 与 n，均值在查询时由 sum / n 得到。
	Mean Kind = iota
	// MeanMax 另存 max：短时尖峰一路保留到最粗一级，不被均值抹平。
	MeanMax
	// Sum 也只存 sum 与 n，但 sum 本身就是可加量（一分钟内的字节增量），查询下发 sum
	// 而不是均值；n 是入账次数，n = 0 与其他列一样表示这一分钟没有入账。
	// Sum 列不从 Metrics 取值：增量由入账方算出后经 AddSum 写入，所以它的 Get 为 nil。
	Sum
)

type Type uint8

const (
	Float Type = iota
	Int
)

type Column struct {
	// Name 是 SQL 列名的词干：cpu → cpu_sum、cpu_n、cpu_max。
	Name string
	Kind Kind
	Type Type
	// Unit 随查询结果下发：percent、bytes、count；load 无单位为空串。
	// 数据自带单位，图表与 agent 都不必查表才知道该怎么读。
	Unit string
	// Get 从一次上报里取读数；false 表示无读数，此时既不进 sum 也不进 n。Sum 列为 nil。
	Get func(*heronv1.Metrics) (float64, bool)
}

func (c Column) SQLType() string {
	if c.Type == Int {
		return "INTEGER"
	}
	return "REAL"
}

func f64(v uint64) float64 { return float64(v) }

// Columns 的顺序就是 Bucket 各切片的下标，也是 SQL 里列的顺序。
// 只能在末尾追加：中间插入会让已存在的桶与行错位。
var Columns = []Column{
	{"cpu", MeanMax, Float, "percent", func(m *heronv1.Metrics) (float64, bool) { return m.GetCpuPct(), m.CpuPct != nil }},
	{"mem_used", MeanMax, Int, "bytes", func(m *heronv1.Metrics) (float64, bool) { return f64(m.GetMemUsed()), m.MemUsed != nil }},
	{"swap_used", Mean, Int, "bytes", func(m *heronv1.Metrics) (float64, bool) { return f64(m.GetSwapUsed()), m.SwapUsed != nil }},
	{"disk_used", Mean, Int, "bytes", func(m *heronv1.Metrics) (float64, bool) { return f64(m.GetDiskUsed()), m.DiskUsed != nil }},
	{"load1", Mean, Float, "", func(m *heronv1.Metrics) (float64, bool) { return m.GetLoad1(), m.Load1 != nil }},
	{"tcp", Mean, Int, "count", func(m *heronv1.Metrics) (float64, bool) { return f64(uint64(m.GetTcpConns())), m.TcpConns != nil }},
	{"udp", Mean, Int, "count", func(m *heronv1.Metrics) (float64, bool) { return f64(uint64(m.GetUdpConns())), m.UdpConns != nil }},
	{"procs", Mean, Int, "count", func(m *heronv1.Metrics) (float64, bool) { return f64(uint64(m.GetProcs())), m.Procs != nil }},
	{"rx_bytes", Sum, Int, "bytes", nil},
	{"tx_bytes", Sum, Int, "bytes", nil},
	{"memory_used_pct", Mean, Float, "percent", func(m *heronv1.Metrics) (float64, bool) { return usedPercent(m.MemUsed, m.MemTotal) }},
	{"disk_used_pct", Mean, Float, "percent", func(m *heronv1.Metrics) (float64, bool) { return usedPercent(m.DiskUsed, m.DiskTotal) }},
}

// 同一次采样的分子、分母必须都存在且容量非零；先算比例再聚合，不能把不同采样的均值相除。
func usedPercent(used, total *uint64) (float64, bool) {
	if used == nil || total == nil || *total == 0 {
		return 0, false
	}
	return float64(*used) / float64(*total) * 100, true
}

// AddSum 把一次入账的可加量加进第 i 列并计一次入账；只对 Sum 列有意义。
func (b *Bucket) AddSum(i int, v float64) {
	b.Sum[i] += v
	b.N[i]++
}

// Index 返回列名在 Columns 里的下标，不存在则 -1。
func Index(name string) int {
	for i, c := range Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// 入账方按下标写 Sum 列，不在热路径上按名字查表。
var (
	RxBytes = Index("rx_bytes")
	TxBytes = Index("tx_bytes")
)

// Bucket 是一分钟内样本的可加折叠。
//
// 全部以 float64 累加，写库时按列类型转回整数。sum 在 2^53 以内是精确的：
// 一分钟最多几十个样本、单个读数不超过 TB 量级，远在这个界内。
type Bucket struct {
	Sum []float64
	N   []uint32
	Max []float64
}

func NewBucket() *Bucket {
	n := len(Columns)
	return &Bucket{Sum: make([]float64, n), N: make([]uint32, n), Max: make([]float64, n)}
}

func (b *Bucket) Add(m *heronv1.Metrics) {
	for i, c := range Columns {
		if c.Get == nil {
			continue
		}
		v, ok := c.Get(m)
		if !ok {
			continue
		}
		b.Sum[i] += v
		if b.N[i] == 0 || v > b.Max[i] {
			b.Max[i] = v
		}
		b.N[i]++
	}
}

// Merge 把 o 加进 b。与写库时的 ON CONFLICT 合并是同一种运算。
func (b *Bucket) Merge(o *Bucket) {
	for i := range Columns {
		if o.N[i] == 0 {
			continue
		}
		if b.N[i] == 0 || o.Max[i] > b.Max[i] {
			b.Max[i] = o.Max[i]
		}
		b.Sum[i] += o.Sum[i]
		b.N[i] += o.N[i]
	}
}

// Mean 的第二个返回值为 false 表示该列在桶内没有任何读数。
func (b *Bucket) Mean(i int) (float64, bool) {
	if b.N[i] == 0 {
		return 0, false
	}
	return b.Sum[i] / float64(b.N[i]), true
}

// Row 是一个时间桶：live 刷出分钟桶，store 也用它返回上卷与查询聚合后的桶。
type Row struct {
	NodeID int64
	// TS 是桶起始，Unix 秒，60 对齐。
	TS     int64
	Bucket *Bucket
	// LastSeen 是该节点最近一次上报的墙钟，写进 node.last_seen_at：供展示与告警文案，hub 重启后还充当抖动窗口判定里的
	// 离线开始（见 alert.Engine.offlineStart），不参与离线时长。
	LastSeen time.Time
	// Source 是该节点最近一次上报的来源地址（auth.SourceText），与 LastSeen 取自同一次上报、一起落盘；空串表示那次
	// 上报取不到对端，不覆盖库里已有的值。
	Source string
}
