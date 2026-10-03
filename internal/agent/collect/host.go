// Package collect 把主机的原始读数变成一次上报。
//
// 平台差异只在 Host 的实现里：Linux 读 /proc 与 /sys（procfs.go），darwin 读 sysctl
// 与 Mach 接口（darwinraw.go 解析，platform_darwin.go 取数）。解析与组合都不带 build tag，
// 任何平台上都能用真机快照或按头文件布局构造的字节测试；带 build tag 的只有系统调用层。
package collect

// Host 是一个平台取原始读数的全部入口。每个方法独立失败，失败只让对应读数缺失。
//
// 合计型读数（conns 合计多张表，ifaces 列出多块网卡）对成员的约定：成员不存在即不计；
// 成员存在却读不出时整个读数返回 error。列出之后、读完之前被删除的网卡也是不存在的成员，
// 怎么认出"已删除"随来源而异，见各实现的 ifaces。缺一块的合计会被当成真值——连接数静默跌落，
// 流量合计变小让 hub 只换基线、恢复时把那一块的历史计数当增量入账（spec §7）；
// 缺失的读数则让面板显示未知、hub 保持基线。两个平台的实现都按这条约定。
//
// 方法名不导出，实现只能在本包内：Linux 的 ProcFS（procfs.go）与 darwin 的
// darwinHost（darwinraw.go）。两次采样的差分、网卡过滤与"读不到即缺失"由此只在 Collector 里实现一次，
// 平台只负责把各自的来源翻译成同一组原始读数。used ≤ total 由 Collector 的 checkUsage 拒收，
// 但它只看得见最终值：中间差的回绕见 memory、swap 的约定。
type Host interface {
	bootID() (string, error)
	// cpuTimes 返回累计 tick，Collector 只用两次读数之差，起点无意义。两次之间 total 不增或
	// idle 变小（如 Linux 的 iowait 会回退）时，由 cpuPercent 放弃这一次读数，不由实现保证单调。
	cpuTimes() (cpuTimes, error)
	// memory、swap 的 used 若由多项先减后加得出，实现必须自己拒收任何一步减法的回绕：
	// 回绕后再加回的值可能落在 total 以内，checkUsage 看不出。
	memory() (usage, error)
	swap() (usage, error)
	disk() (usage, error)
	load() (loadAvg, error)
	procs() (uint32, error)
	uptime() (uint64, error)
	conns() (tcp, udp uint32, err error)
	// ifaces 返回全部网卡，不做过滤：哪些网卡计入流量只由 Collector.includeIface 决定，
	// 实现里漏掉或写错过滤不会把被排除的网卡带进合计。
	ifaces() ([]ifaceCounters, error)
	// diskCounters 返回整盘设备的累计读/写字节，分区与 loop/ram/zram、dm/md 设备不计。
	// 两次采样的差分、设备集合变化与"读不到即缺失"由 Collector 统一处理；平台不提供该读数
	// （darwin）时返回 errNoDiskCounters，Collector 据此只让两项缺失，不记失败。
	diskCounters() ([]diskCounters, error)
	// defaultNetExclude 是未给 --net-exclude 时不计入流量的网卡；网卡命名随平台而异。
	defaultNetExclude() []string
	facts() hostFacts
}

type usage struct{ total, used uint64 }

type ifaceCounters struct {
	name   string
	rx, tx uint64
}

// diskCounters 是一块整盘设备的累计读/写字节；Collector 只用两次读数之差。
type diskCounters struct {
	name        string
	read, write uint64
}

type hostFacts struct {
	hostname, os, kernel, virtualization, cpuModel string
	cpuCores                                       uint32
}
