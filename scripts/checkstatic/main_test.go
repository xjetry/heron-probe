package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"debug/buildinfo"
	"runtime/debug"
)

func buildHello(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "hello")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/hello")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("build testdata/hello: %v", err)
	}
	return out
}

// 宿主 macOS 没有交叉 C 工具链，产不出真正动态链接的 Linux 二进制；
// 动态加载器这条特征只能用手工合成的 ELF64 小端文件表达。
func writeInterpELF(t *testing.T) string {
	t.Helper()
	interp := []byte("/lib/ld-musl-x86_64.so.1\x00")

	var buf bytes.Buffer
	ident := make([]byte, 16)
	ident[0], ident[1], ident[2], ident[3] = 0x7f, 'E', 'L', 'F'
	ident[4] = 2 // ELFCLASS64
	ident[5] = 1 // ELFDATA2LSB
	ident[6] = 1 // EV_CURRENT
	buf.Write(ident)

	le := binary.Write
	le(&buf, binary.LittleEndian, uint16(2))  // e_type = ET_EXEC
	le(&buf, binary.LittleEndian, uint16(62)) // e_machine = EM_X86_64
	le(&buf, binary.LittleEndian, uint32(1))  // e_version
	le(&buf, binary.LittleEndian, uint64(0))  // e_entry
	le(&buf, binary.LittleEndian, uint64(64)) // e_phoff，紧跟文件头
	le(&buf, binary.LittleEndian, uint64(0))  // e_shoff
	le(&buf, binary.LittleEndian, uint32(0))  // e_flags
	le(&buf, binary.LittleEndian, uint16(64)) // e_ehsize
	le(&buf, binary.LittleEndian, uint16(56)) // e_phentsize
	le(&buf, binary.LittleEndian, uint16(1))  // e_phnum
	le(&buf, binary.LittleEndian, uint16(64)) // e_shentsize
	le(&buf, binary.LittleEndian, uint16(0))  // e_shnum
	le(&buf, binary.LittleEndian, uint16(0))  // e_shstrndx

	off := uint64(64 + 56)
	le(&buf, binary.LittleEndian, uint32(3))           // p_type = PT_INTERP
	le(&buf, binary.LittleEndian, uint32(4))           // p_flags = PF_R
	le(&buf, binary.LittleEndian, off)                 // p_offset
	le(&buf, binary.LittleEndian, off)                 // p_vaddr
	le(&buf, binary.LittleEndian, off)                 // p_paddr
	le(&buf, binary.LittleEndian, uint64(len(interp))) // p_filesz
	le(&buf, binary.LittleEndian, uint64(len(interp))) // p_memsz
	le(&buf, binary.LittleEndian, uint64(1))           // p_align
	buf.Write(interp)

	path := filepath.Join(t.TempDir(), "interp.elf")
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatalf("write interp elf: %v", err)
	}
	return path
}

func TestCheckStaticBinaryHasNoReasons(t *testing.T) {
	if reasons := check(buildHello(t)); len(reasons) != 0 {
		t.Fatalf("static linux build reported reasons: %v", reasons)
	}
}

func TestCheckInterpELF(t *testing.T) {
	reasons := strings.Join(check(writeInterpELF(t)), "; ")
	if !strings.Contains(reasons, "has PT_INTERP") {
		t.Fatalf("want has PT_INTERP, got %q", reasons)
	}
	if !strings.Contains(reasons, "/lib/ld-musl-x86_64.so.1") {
		t.Fatalf("want loader path in reason, got %q", reasons)
	}
}

func TestCgoReason(t *testing.T) {
	cgoOn := &buildinfo.BuildInfo{Settings: []debug.BuildSetting{{Key: "CGO_ENABLED", Value: "1"}}}
	if got := strings.Join(cgoReason(cgoOn), "; "); !strings.Contains(got, "built with CGO_ENABLED=1") {
		t.Fatalf("want built with CGO_ENABLED=1, got %q", got)
	}
	// 构建设置缺失不等于 0，不能放行。
	noSetting := &buildinfo.BuildInfo{}
	if got := strings.Join(cgoReason(noSetting), "; "); !strings.Contains(got, "missing CGO_ENABLED build setting") {
		t.Fatalf("want missing CGO_ENABLED build setting, got %q", got)
	}
}

func TestCheckNotELF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-an-elf")
	if err := os.WriteFile(path, []byte("just some text\n"), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
	}
	reasons := strings.Join(check(path), "; ")
	if !strings.Contains(reasons, "not an ELF file") {
		t.Fatalf("want not an ELF file, got %q", reasons)
	}
}
