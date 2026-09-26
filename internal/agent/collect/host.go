// Package collect 把主机的原始读数变成一次上报。
//
// 平台差异只在 Host 的实现里：Linux 读 /proc 与 /sys（procfs.go），darwin 读 sysctl
// 与 Mach 接口（darwinraw.go 解析，platform_darwin.go 取数）。解析与组合都不带 build tag，
// 任何平台上都能用真机快照或按头文件布局构造的字节测试；带 build tag 的只有系统调用层。
package collect

// Host 是一个平台取原始读数的全部入口。每个方法独立失败，失败只让对应读数缺失。
//
// 方法名不导出，实现只能在本包内：Linux 的 ProcFS（procfs.go）与 darwin 的
// darwinHost（darwinraw.go）。两次采样的差分、网卡过滤、用量不变式与"读不到即缺失"
// 由此只在 Collector 里实现一次，平台只负责把各自的来源翻译成同一组原始读数。
type Host interface {
	bootID() (string, error)
	// cpuTimes 返回单调不减的累计 tick；Collector 只用两次读数之差，起点无意义。
	cpuTimes() (cpuTimes, error)
	memory() (usage, error)
	swap() (usage, error)
	disk() (usage, error)
	load() (loadAvg, error)
	procs() (uint32, error)
	uptime() (uint64, error)
	conns() (tcp, udp uint32, err error)
	// ifaces 只返回 include 接受的网卡：能按名字先过滤的平台不必读被排除网卡的计数器。
	ifaces(include func(name string) bool) ([]ifaceCounters, error)
	// defaultNetExclude 是未给 --net-exclude 时不计入流量的网卡；网卡命名随平台而异。
	defaultNetExclude() []string
	facts() hostFacts
}

type usage struct{ total, used uint64 }

type ifaceCounters struct {
	name   string
	rx, tx uint64
}

type hostFacts struct {
	hostname, os, kernel, virtualization, cpuModel string
	cpuCores                                       uint32
}
