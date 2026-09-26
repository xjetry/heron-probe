// checkimage 核对 hub 镜像的根文件系统。输入是 buildx 以 --output type=tar 导出的多平台结果：
// 每个平台一个 linux_<arch>/ 目录，里面是该平台镜像的完整文件树，不含容器运行时注入的 /dev、/proc 等。
//
// spec §14 要求镜像只含静态二进制、CA 证书与非 root 用户。这里逐条目核对类型、权限与属主，缺少或
// 多出任何条目都拒绝：往镜像里加东西必须同时改 want，这份清单与 Dockerfile 一起审阅。
package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

// runUID 是镜像的运行用户与主组，与 Dockerfile 的 USER、rootfs 阶段写进 /etc/passwd 的数同一个。
const runUID = 65532

// minCACerts：Alpine 3.21 的证书包在 2026-09-26 有 121 张根证书。远少于此说明拿到的不是完整的
// 系统证书包，通知出站 HTTPS 会对许多接收方校验失败。
const minCACerts = 100

const (
	passwdPath = "etc/passwd"
	groupPath  = "etc/group"
	caPath     = "etc/ssl/certs/ca-certificates.crt"
	binPath    = "usr/local/bin/probe-hub"
)

type entry struct {
	mode     fs.FileMode // 类型位、权限位与 sticky 位
	uid, gid int
}

var want = map[string]entry{
	// 空卷挂到 /data 时 Docker 沿用镜像里这个目录的属主，hub 以 runUID 写库。
	"data":          {fs.ModeDir | 0o755, runUID, runUID},
	"etc":           {fs.ModeDir | 0o755, 0, 0},
	groupPath:       {0o644, 0, 0},
	passwdPath:      {0o644, 0, 0},
	"etc/ssl":       {fs.ModeDir | 0o755, 0, 0},
	"etc/ssl/certs": {fs.ModeDir | 0o755, 0, 0},
	caPath:          {0o644, 0, 0},
	// SQLite 找不到可写的临时目录时，排序溢出、临时表与建索引都报 disk I/O error (6410)。
	"tmp":           {fs.ModeDir | fs.ModeSticky | 0o777, 0, 0},
	"usr":           {fs.ModeDir | 0o755, 0, 0},
	"usr/local":     {fs.ModeDir | 0o755, 0, 0},
	"usr/local/bin": {fs.ModeDir | 0o755, 0, 0},
	binPath:         {0o755, 0, 0},
}

// machines：镜像索引的平台标签来自构建时的 TARGETARCH，Dockerfile 按它取二进制。二者不符时，
// 能经 binfmt 执行其他架构的宿主照样能把容器跑起来，只有 ELF 头能揭示。
var machines = map[string]elf.Machine{
	"amd64": elf.EM_X86_64,
	"arm64": elf.EM_AARCH64,
}

func check(r io.Reader, arches []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	expected := map[string]string{} // 平台目录 → GOARCH
	for _, a := range arches {
		if _, ok := machines[a]; !ok {
			add("%s: no ELF machine known for this architecture; add it to machines", a)
			continue
		}
		expected["linux_"+a] = a
	}

	headers := map[string]map[string]*tar.Header{}
	bodies := map[string][]byte{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			add("read tar: %v", err)
			return problems
		}
		name := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
		platform, rel, _ := strings.Cut(name, "/")
		if headers[platform] == nil {
			headers[platform] = map[string]*tar.Header{}
		}
		if rel == "" {
			continue
		}
		headers[platform][rel] = hdr
		switch rel {
		case passwdPath, groupPath, caPath, binPath:
			b, err := io.ReadAll(tr)
			if err != nil {
				add("%s: read: %v", name, err)
				continue
			}
			bodies[name] = b
		}
	}

	for _, platform := range slices.Sorted(maps.Keys(headers)) {
		if _, ok := expected[platform]; !ok {
			add("%s: unexpected platform", platform)
		}
	}
	for _, platform := range slices.Sorted(maps.Keys(expected)) {
		got, ok := headers[platform]
		if !ok {
			add("%s: missing platform", platform)
			continue
		}
		for _, rel := range slices.Sorted(maps.Keys(want)) {
			w := want[rel]
			hdr, ok := got[rel]
			if !ok {
				add("%s/%s: missing", platform, rel)
				continue
			}
			if m := hdr.FileInfo().Mode(); m != w.mode {
				add("%s/%s: mode %v, want %v", platform, rel, m, w.mode)
			}
			if hdr.Uid != w.uid || hdr.Gid != w.gid {
				add("%s/%s: owner %d:%d, want %d:%d", platform, rel, hdr.Uid, hdr.Gid, w.uid, w.gid)
			}
		}
		for _, rel := range slices.Sorted(maps.Keys(got)) {
			if _, ok := want[rel]; !ok {
				add("%s/%s: unexpected entry", platform, rel)
			}
		}
		if b, ok := bodies[platform+"/"+passwdPath]; ok {
			if err := checkAccount(b, 7); err != nil {
				add("%s/%s: %v", platform, passwdPath, err)
			}
		}
		if b, ok := bodies[platform+"/"+groupPath]; ok {
			if err := checkAccount(b, 4); err != nil {
				add("%s/%s: %v", platform, groupPath, err)
			}
		}
		if b, ok := bodies[platform+"/"+caPath]; ok {
			if n := countCerts(b); n < minCACerts {
				add("%s/%s: %d certificates, want at least %d", platform, caPath, n, minCACerts)
			}
		}
		if b, ok := bodies[platform+"/"+binPath]; ok {
			machine := machines[expected[platform]]
			f, err := elf.NewFile(bytes.NewReader(b))
			switch {
			case err != nil:
				add("%s/%s: not an ELF file: %v", platform, binPath, err)
			case f.Machine != machine:
				add("%s/%s: ELF machine %v, want %v", platform, binPath, f.Machine, machine)
			}
		}
	}
	return problems
}

// checkAccount 要求文件里只列运行用户一个账户：passwd 七列、group 四列，第三列（uid 或 gid）为 runUID，
// passwd 的第四列（主组）也为 runUID。
func checkAccount(b []byte, fields int) error {
	line := strings.TrimSuffix(string(b), "\n")
	if line == "" || strings.Contains(line, "\n") {
		return fmt.Errorf("want exactly one account, got %q", line)
	}
	f := strings.Split(line, ":")
	if len(f) != fields {
		return fmt.Errorf("malformed line %q", line)
	}
	id := strconv.Itoa(runUID)
	if f[2] != id || (fields == 7 && f[3] != id) {
		return fmt.Errorf("account %q is not %s:%s", line, id, id)
	}
	return nil
}

// countCerts 数 PEM 里的 CERTIFICATE 块；块之间的注释行由 pem.Decode 跳过。
func countCerts(b []byte) int {
	n := 0
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			return n
		}
		if block.Type == "CERTIFICATE" {
			n++
		}
	}
}

// run 是可测的 CLI 入口。架构清单为空时没有平台可核对，等价于"全部通过"，必须在入口拒绝。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: checkimage <rootfs.tar> <arch>...")
		return 2
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer f.Close()
	problems := check(f, args[1:])
	for _, p := range problems {
		fmt.Fprintln(stdout, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
