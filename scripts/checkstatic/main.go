// Alpine 用 musl，动态链接 glibc 的二进制在那里直接无法启动；原生 Linux 上构建时
// CGO_ENABLED 默认为 1，net 包会链接系统解析器，所以只靠构建命令的约定不够，要在产物上检查。
package main

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
)

func check(path string) []string {
	var reasons []string
	f, err := elf.Open(path)
	if err != nil {
		return append(reasons, fmt.Sprintf("not an ELF file: %v", err))
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		data := make([]byte, p.Filesz)
		if _, err := p.ReadAt(data, 0); err != nil {
			return append(reasons, fmt.Sprintf("has PT_INTERP (unreadable: %v)", err))
		}
		reasons = append(reasons, fmt.Sprintf("has PT_INTERP (dynamic loader %s)", string(bytes.TrimRight(data, "\x00"))))
	}
	if libs, err := f.ImportedLibraries(); err == nil {
		for _, lib := range libs {
			reasons = append(reasons, fmt.Sprintf("has DT_NEEDED %s", lib))
		}
	}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		// 非 Go 产物或读不出构建设置：无法证明 CGO_ENABLED=0，按缺失处理。
		return append(reasons, "missing CGO_ENABLED build setting")
	}
	return append(reasons, cgoReason(bi)...)
}

func cgoReason(bi *buildinfo.BuildInfo) []string {
	if bi == nil {
		return []string{"missing CGO_ENABLED build setting"}
	}
	for _, s := range bi.Settings {
		if s.Key != "CGO_ENABLED" {
			continue
		}
		if s.Value != "0" {
			return []string{fmt.Sprintf("built with CGO_ENABLED=%s", s.Value)}
		}
		return nil
	}
	// 设置缺失不等于 0，不能放行。
	return []string{"missing CGO_ENABLED build setting"}
}

func main() {
	status := 0
	for _, path := range os.Args[1:] {
		for _, reason := range check(path) {
			fmt.Printf("%s: %s\n", path, reason)
			status = 1
		}
	}
	os.Exit(status)
}
