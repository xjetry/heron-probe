package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type file struct {
	name     string
	typ      byte
	mode     int64
	uid, gid int
	body     []byte
}

// fakeELF 只有 64 字节的 ELF 文件头：checkimage 只看机器类型，合成夹具不依赖交叉工具链。
func fakeELF(m elf.Machine) []byte {
	var buf bytes.Buffer
	h := elf.Header64{
		Ident:     [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)},
		Type:      uint16(elf.ET_EXEC),
		Machine:   uint16(m),
		Version:   uint32(elf.EV_CURRENT),
		Ehsize:    64,
		Phentsize: 56,
		Shentsize: 64,
	}
	if err := binary.Write(&buf, binary.LittleEndian, h); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// caBundle 由 n 个 CERTIFICATE 块组成；checkimage 只数块，不解析证书内容。
func caBundle(n int) []byte {
	return bytes.Repeat(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not parsed")}), n)
}

func dir(name string, mode int64, owner int) file {
	return file{name: name, typ: tar.TypeDir, mode: mode, uid: owner, gid: owner}
}

func reg(name string, mode int64, body []byte) file {
	return file{name: name, typ: tar.TypeReg, mode: mode, body: body}
}

// expectedLayout 与 Dockerfile 产出的根文件系统一致，按 buildx 的 tar 输出分平台目录。
func expectedLayout(arches ...string) []file {
	var files []file
	for _, a := range arches {
		p := "linux_" + a + "/"
		files = append(files,
			dir(p, 0o755, 0),
			dir(p+"data/", 0o755, runUID),
			dir(p+"etc/", 0o755, 0),
			reg(p+"etc/group", 0o644, []byte("probe-hub:x:65532:\n")),
			reg(p+"etc/passwd", 0o644, []byte("probe-hub:x:65532:65532:probe-hub:/nonexistent:/sbin/nologin\n")),
			dir(p+"etc/ssl/", 0o755, 0),
			dir(p+"etc/ssl/certs/", 0o755, 0),
			reg(p+"etc/ssl/certs/ca-certificates.crt", 0o644, caBundle(minCACerts)),
			dir(p+"tmp/", 0o1777, 0),
			dir(p+"usr/", 0o755, 0),
			dir(p+"usr/local/", 0o755, 0),
			dir(p+"usr/local/bin/", 0o755, 0),
			reg(p+"usr/local/bin/probe-hub", 0o755, fakeELF(machines[a])),
		)
	}
	return files
}

func tarOf(t *testing.T, files []file) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Typeflag: f.typ, Mode: f.mode, Uid: f.uid, Gid: f.gid, Size: int64(len(f.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func edit(name string, change func(*file)) func([]file) []file {
	return func(files []file) []file {
		for i := range files {
			if files[i].name == name {
				change(&files[i])
			}
		}
		return files
	}
}

func without(prefix string) func([]file) []file {
	return func(files []file) []file {
		var kept []file
		for _, f := range files {
			if !strings.HasPrefix(f.name, prefix) {
				kept = append(kept, f)
			}
		}
		return kept
	}
}

func with(extra file) func([]file) []file {
	return func(files []file) []file { return append(files, extra) }
}

func TestAcceptsTheExpectedLayout(t *testing.T) {
	if problems := check(tarOf(t, expectedLayout("amd64", "arm64")), []string{"amd64", "arm64"}); len(problems) != 0 {
		t.Fatalf("problems on the expected layout: %q", problems)
	}
}

func TestRejectsEachDeviation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]file) []file
		want   string
	}{
		{"tmp without the sticky bit", edit("linux_amd64/tmp/", func(f *file) { f.mode = 0o777 }), "linux_amd64/tmp: mode 0777, want 1777"},
		{"no tmp", without("linux_arm64/tmp/"), "linux_arm64/tmp: missing"},
		{"data owned by root", edit("linux_amd64/data/", func(f *file) { f.uid, f.gid = 0, 0 }), "linux_amd64/data: owner 0:0, want 65532:65532"},
		{"binary not executable", edit("linux_amd64/usr/local/bin/probe-hub", func(f *file) { f.mode = 0o644 }), "linux_amd64/usr/local/bin/probe-hub: mode 0644, want 0755"},
		{"a shell", with(reg("linux_amd64/bin/sh", 0o755, nil)), "linux_amd64/bin/sh: unexpected entry"},
		{"binary for the other architecture", edit("linux_arm64/usr/local/bin/probe-hub", func(f *file) { f.body = fakeELF(elf.EM_X86_64) }), "linux_arm64/usr/local/bin/probe-hub: ELF machine EM_X86_64, want EM_AARCH64"},
		{"binary that is not ELF", edit("linux_amd64/usr/local/bin/probe-hub", func(f *file) { f.body = []byte("#!/bin/sh\n") }), "linux_amd64/usr/local/bin/probe-hub: not an ELF file"},
		{"missing platform", without("linux_arm64/"), "linux_arm64: missing platform"},
		{"extra platform", with(dir("linux_386/", 0o755, 0)), "linux_386: unexpected platform"},
		{"short CA bundle", edit("linux_amd64/etc/ssl/certs/ca-certificates.crt", func(f *file) { f.body = caBundle(1) }), "linux_amd64/etc/ssl/certs/ca-certificates.crt: 1 certificates, want at least 100"},
		{"root account", edit("linux_amd64/etc/passwd", func(f *file) { f.body = []byte("root:x:0:0:root:/root:/bin/sh\n") }), "linux_amd64/etc/passwd: account"},
		{"a second account", edit("linux_arm64/etc/passwd", func(f *file) { f.body = append(f.body, "root:x:0:0:root:/root:/bin/sh\n"...) }), "linux_arm64/etc/passwd: want exactly one account"},
		{"root group", edit("linux_amd64/etc/group", func(f *file) { f.body = []byte("root:x:0:\n") }), "linux_amd64/etc/group: account"},
		{"passwd with root as the primary group", edit("linux_amd64/etc/passwd", func(f *file) { f.body = []byte("probe-hub:x:65532:0:probe-hub:/nonexistent:/sbin/nologin\n") }), "linux_amd64/etc/passwd: account"},
		{"passwd with an extra column", edit("linux_amd64/etc/passwd", func(f *file) { f.body = []byte("probe-hub:x:65532:65532:probe-hub:/nonexistent:/sbin/nologin:x\n") }), "linux_amd64/etc/passwd: malformed line"},
		{"tmp as a regular file", edit("linux_amd64/tmp/", func(f *file) { f.name, f.typ = "linux_amd64/tmp", tar.TypeReg }), "linux_amd64/tmp: type regular file, want directory"},
		{"passwd as a hard link", edit("linux_arm64/etc/passwd", func(f *file) { f.typ, f.body = tar.TypeLink, nil }), "linux_arm64/etc/passwd: type hard link, want regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := check(tarOf(t, tc.mutate(expectedLayout("amd64", "arm64"))), []string{"amd64", "arm64"})
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Fatalf("no problem contains %q; got %q", tc.want, problems)
		})
	}
}

// 读 tar 出错时 check 提前返回、跳过其余核对，这一条问题是损坏输入唯一的失败信号。
func TestReportsAnUnreadableArchive(t *testing.T) {
	problems := check(bytes.NewReader(bytes.Repeat([]byte("not a tar archive\n"), 64)), []string{"amd64"})
	for _, p := range problems {
		if strings.HasPrefix(p, "read tar: ") {
			return
		}
	}
	t.Fatalf("no problem starts with \"read tar: \"; got %q", problems)
}

func TestRunRejectsAnEmptyArchitectureList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"rootfs.tar"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d without architectures, want 2", code)
	}
}

func TestRunExitCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.tar")
	if err := os.WriteFile(path, tarOf(t, expectedLayout("amd64")).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{path, "amd64"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("expected layout: exit %d, stdout %q", code, stdout.String())
	}
	stdout.Reset()
	if code := run([]string{path, "amd64", "riscv64"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "riscv64: no ELF machine known") {
		t.Fatalf("unknown architecture: exit %d, stdout %q", code, stdout.String())
	}
}
