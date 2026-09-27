// Package themetest 用 Go 生成主题包夹具，供 theme 与 api 的测试共用：夹具随代码生成，不入库二进制文件，
// 每个用例需要的畸形（链接条目、撒谎的中央目录）都能在这里精确构造。
package themetest

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/json"
	"hash/crc32"
	"io"
	"io/fs"
	"testing"
)

// PNG 以 PNG 签名开头，http.DetectContentType 把它嗅探为 image/png。
var PNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// Entry 是一个 zip 条目。Mode 非零时经 SetMode 写入外部属性（符号链接、设备等）；Declared 非零时按原始数据写入，
// 中央目录声明的未压缩大小取 Declared 而不是内容的真实长度——撒谎的中央目录。
type Entry struct {
	Name     string
	Content  []byte
	Mode     fs.FileMode
	Declared uint64
	// Method 为 zip.Store 时不压缩；默认 deflate。
	Method uint16
}

// File 是一个带内容的普通文件条目。
func File(name, content string) Entry { return Entry{Name: name, Content: []byte(content)} }

// Manifest 生成 theme.json 条目；preview 为空时不写该字段。
func Manifest(t testing.TB, id, name, version, preview string) Entry {
	t.Helper()
	m := map[string]string{"id": id, "name": name, "version": version}
	if preview != "" {
		m["preview"] = preview
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Entry{Name: "theme.json", Content: b}
}

// Minimal 是最小的合法主题：清单加 index.html，extra 追加在后面。
func Minimal(t testing.TB, id string, extra ...Entry) []byte {
	t.Helper()
	return Zip(t, append([]Entry{Manifest(t, id, "Theme "+id, "1.0.0", ""), File("index.html", "<!doctype html>"+id)}, extra...)...)
}

// Zip 按顺序写出条目。
func Zip(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	// 最快档：大夹具是几十 MiB 的零字节，任何档位都压得很小，高档位只多花时间。
	w.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestSpeed) })
	for _, e := range entries {
		method := e.Method
		if method == 0 && e.Content != nil {
			method = zip.Deflate
		}
		if e.Declared != 0 {
			var comp bytes.Buffer
			fw, err := flate.NewWriter(&comp, flate.BestSpeed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(e.Content); err != nil {
				t.Fatal(err)
			}
			if err := fw.Close(); err != nil {
				t.Fatal(err)
			}
			h := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(e.Content),
				CompressedSize64: uint64(comp.Len()), UncompressedSize64: e.Declared}
			raw, err := w.CreateRaw(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Write(comp.Bytes()); err != nil {
				t.Fatal(err)
			}
			continue
		}
		h := &zip.FileHeader{Name: e.Name, Method: method}
		if e.Mode != 0 {
			h.SetMode(e.Mode)
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(e.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
