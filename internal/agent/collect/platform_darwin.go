//go:build darwin

package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{Host: &darwinHost{src: openDarwinSyscalls()}, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// DefaultNetExclude 是本平台未给 --net-exclude 时不计入流量的网卡。
func DefaultNetExclude() []string { return darwinNetExclude }

// Mach 常量取自 <mach/processor_info.h>、<mach/host_info.h>、<mach/vm_statistics.h>、<mach/kern_return.h>。
const (
	processorCPULoadInfo = 2  // PROCESSOR_CPU_LOAD_INFO
	hostVMInfo64         = 4  // HOST_VM_INFO64
	hostVMInfo64Count    = 38 // HOST_VM_INFO64_COUNT = sizeof(vm_statistics64_data_t) / sizeof(integer_t)
	kernSuccess          = 0
)

// darwinSyscalls 是 darwinSource 的真实实现：sysctl 走 x/sys/unix；逐 CPU 计数与 VM 统计只有 Mach 接口给得出，
// 进程数用 libproc 的 proc_listallpids（sysctl kern.proc.all 也能数，但每次要复制约 1 MB 的 kinfo_proc），
// 这几个经 purego 调 libSystem。
type darwinSyscalls struct {
	// machErr 非空时 libSystem 的函数不可用：依赖它们的读数缺失，sysctl 读数照常，agent 不因此退出。
	machErr error
	// mach_host_self 每次调用都给同一个端口名多加一个发送权引用，只在打开时取一次。
	host, task uint32

	hostProcessorInfo func(host uint32, flavor int32, n *uint32, info **uint32, cnt *uint32) int32
	hostStatistics64  func(host uint32, flavor int32, info *uint32, cnt *uint32) int32
	hostPageSize      func(host uint32, size *uintptr) int32
	vmDeallocate      func(task uint32, addr, size uintptr) int32
	procListallpids   func(buf *int32, size int32) int32
}

func openDarwinSyscalls() *darwinSyscalls {
	s := &darwinSyscalls{}
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		s.machErr = fmt.Errorf("dlopen libSystem: %w", err)
		return s
	}
	// 用 Dlsym 逐个取符号再注册：RegisterLibFunc 找不到符号时 panic，而缺一个函数只该让对应读数缺失。
	bind := func(fptr any, name string) {
		if s.machErr != nil {
			return
		}
		sym, err := purego.Dlsym(lib, name)
		if err != nil {
			s.machErr = fmt.Errorf("dlsym %s: %w", name, err)
			return
		}
		purego.RegisterFunc(fptr, sym)
	}
	var machHostSelf, machTaskSelf func() uint32
	bind(&machHostSelf, "mach_host_self")
	bind(&machTaskSelf, "mach_task_self")
	bind(&s.hostProcessorInfo, "host_processor_info")
	bind(&s.hostStatistics64, "host_statistics64")
	bind(&s.hostPageSize, "host_page_size")
	bind(&s.vmDeallocate, "vm_deallocate")
	bind(&s.procListallpids, "proc_listallpids")
	if s.machErr == nil {
		s.host, s.task = machHostSelf(), machTaskSelf()
	}
	return s
}

func (s *darwinSyscalls) sysctlString(name string) (string, error) { return unix.Sysctl(name) }
func (s *darwinSyscalls) sysctlUint32(name string) (uint32, error) { return unix.SysctlUint32(name) }
func (s *darwinSyscalls) sysctlUint64(name string) (uint64, error) { return unix.SysctlUint64(name) }
func (s *darwinSyscalls) sysctlRaw(name string, args ...int) ([]byte, error) {
	return unix.SysctlRaw(name, args...)
}
func (s *darwinSyscalls) statfs(path string) (uint64, uint64, error) { return statfs(path) }

// monotonicSeconds 用 CLOCK_MONOTONIC：macOS 上它自启动起算且含睡眠时间，与 now − kern.boottime 一致，
// 又不随墙钟调整。CLOCK_UPTIME_RAW 不含睡眠（实测一台睡过的机器上两者差 14 小时），不能当运行时长。
func (s *darwinSyscalls) monotonicSeconds() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, err
	}
	return uint64(ts.Sec), nil
}

// processorTicks 用逐 CPU 的 host_processor_info 而不是聚合的 host_statistics(HOST_CPU_LOAD_INFO)：
// 实测一台 16 核 Apple Silicon：前者之和每秒稳定在约 1590（CPU 数 × kern.clockrate 的 hz 100），
// 后者按秒取差在 1057 到 2202 之间摆动，算出的忙碌比例偏差到 12 个百分点。
func (s *darwinSyscalls) processorTicks() ([]uint32, error) {
	if s.machErr != nil {
		return nil, s.machErr
	}
	var n, cnt uint32
	var info *uint32
	if kr := s.hostProcessorInfo(s.host, processorCPULoadInfo, &n, &info, &cnt); kr != kernSuccess {
		return nil, fmt.Errorf("host_processor_info: kern_return_t %d", kr)
	}
	// 数组由内核在本进程地址空间里 vm_allocate，复制出来后必须归还，否则每次采样都泄漏。
	out := slices.Clone(unsafe.Slice(info, cnt))
	if kr := s.vmDeallocate(s.task, uintptr(unsafe.Pointer(info)), uintptr(cnt)*4); kr != kernSuccess {
		return nil, fmt.Errorf("vm_deallocate: kern_return_t %d", kr)
	}
	if uint64(cnt) != uint64(n)*cpuStateMax {
		return nil, fmt.Errorf("host_processor_info: %d values for %d cpus", cnt, n)
	}
	return out, nil
}

func (s *darwinSyscalls) vmStatistics64() ([]byte, error) {
	if s.machErr != nil {
		return nil, s.machErr
	}
	var words [hostVMInfo64Count]uint32
	cnt := uint32(hostVMInfo64Count)
	if kr := s.hostStatistics64(s.host, hostVMInfo64, &words[0], &cnt); kr != kernSuccess {
		return nil, fmt.Errorf("host_statistics64: kern_return_t %d", kr)
	}
	// 按字重新编码成小端字节，交给与头文件布局对照的解析；darwin 的两个目标都是小端，逐字编码与内存布局相同。
	b := make([]byte, 4*int(cnt))
	for i := range int(cnt) {
		binary.LittleEndian.PutUint32(b[4*i:], words[i])
	}
	return b, nil
}

// pageSize 取 host_page_size：它是 host_statistics64 页计数的单位。hw.pagesize 对 Rosetta 转译的
// x86_64 进程报 4096，而 Apple Silicon 内核的页是 16384，用它会把已用内存少算四倍。
func (s *darwinSyscalls) pageSize() (uint64, error) {
	if s.machErr != nil {
		return 0, s.machErr
	}
	var size uintptr
	if kr := s.hostPageSize(s.host, &size); kr != kernSuccess {
		return 0, fmt.Errorf("host_page_size: kern_return_t %d", kr)
	}
	return uint64(size), nil
}

// pidCount 用 proc_listallpids：不带缓冲区时返回带余量的估计值，带缓冲区时返回实际写入的个数。
// 写满缓冲区说明进程表在两次调用之间长过了余量，放大再取。
func (s *darwinSyscalls) pidCount() (uint32, error) {
	if s.machErr != nil {
		return 0, s.machErr
	}
	est := s.procListallpids(nil, 0)
	if est <= 0 {
		return 0, errors.New("proc_listallpids: no estimate")
	}
	size := int(est) + 64
	for range 3 {
		buf := make([]int32, size)
		got := s.procListallpids(&buf[0], int32(4*len(buf)))
		if got <= 0 {
			return 0, fmt.Errorf("proc_listallpids: returned %d", got)
		}
		if int(got) < len(buf) {
			return uint32(got), nil
		}
		size *= 2
	}
	return 0, errors.New("proc_listallpids: process table kept outgrowing the buffer")
}
