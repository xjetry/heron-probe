package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/githubtransport"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testSource(t *testing.T, routes map[string][]byte) *OfficialSource {
	t.Helper()
	return &OfficialSource{http: githubtransport.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := routes[r.URL.String()]
		if !ok {
			t.Fatalf("unexpected request %s", r.URL)
		}
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") != "heron-official-updater" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Fatalf("unexpected request headers: %v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header), Request: r}, nil
	}), time.Second)}
}

func makeArchive(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(bodies[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func validArchive(t *testing.T, role string) []byte {
	return makeArchive(t, []*tar.Header{{Name: "heron-" + role, Mode: 0755, Size: 6, Typeflag: tar.TypeReg}, {Name: "heron-" + role + ".service", Mode: 0644, Size: 4, Typeflag: tar.TypeReg}}, []string{"binary", "unit"})
}

func releaseRoutes(t *testing.T, role, arch string, archive []byte) map[string][]byte {
	t.Helper()
	asset, err := archiveName(role, arch)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), asset))
	metadata := []byte(fmt.Sprintf(`{"tag_name":"v0.3.0","assets":[{"name":%q,"size":%d},{"name":"SHA256SUMS","size":%d}]}`, asset, len(archive), len(sums)))
	return map[string][]byte{officialAPI + "/latest": metadata, officialAPI + "/tags/v0.3.0": metadata, officialDownloads + "v0.3.0/SHA256SUMS": sums, officialDownloads + "v0.3.0/" + asset: archive}
}

func TestOfficialDownload(t *testing.T) {
	for _, pair := range [][2]string{{"hub", "amd64"}, {"hub", "arm64"}, {"agent", "amd64"}, {"agent", "arm64"}, {"agent", "armv7"}, {"agent", "386"}, {"agent", "riscv64"}} {
		t.Run(strings.Join(pair[:], "/"), func(t *testing.T) {
			s := testSource(t, releaseRoutes(t, pair[0], pair[1], validArchive(t, pair[0])))
			version, err := s.Latest(context.Background())
			if err != nil || version != "v0.3.0" {
				t.Fatalf("Latest = %q, %v", version, err)
			}
			body, err := s.Download(context.Background(), pair[0], pair[1], version)
			if err != nil || string(body) != "binary" {
				t.Fatalf("Download = %q, %v", body, err)
			}
		})
	}
}

func TestOfficialRejectsInputBeforeNetwork(t *testing.T) {
	s := testSource(t, nil)
	for _, args := range [][3]string{{"hub", "386", "v0.3.0"}, {"agent", "mips", "v0.3.0"}, {"updater", "amd64", "v0.3.0"}, {"hub", "amd64", "../../other"}, {"hub", "amd64", "v0.3.0-rc1"}, {"hub", "amd64", "v00.3.0"}} {
		if _, err := s.Download(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestOfficialRejectsReleaseMetadata(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"draft":             func(m map[string]any) { m["draft"] = true },
		"prerelease":        func(m map[string]any) { m["prerelease"] = true },
		"invalid version":   func(m map[string]any) { m["tag_name"] = "v0.3.0-rc1" },
		"tag mismatch":      func(m map[string]any) { m["tag_name"] = "v0.4.0" },
		"missing assets":    func(m map[string]any) { m["assets"] = []any{} },
		"duplicate asset":   func(m map[string]any) { a := m["assets"].([]any); m["assets"] = append(a, a[0]) },
		"oversized archive": func(m map[string]any) { m["assets"].([]any)[0].(map[string]any)["size"] = maxArchive + 1 },
		"oversized sums":    func(m map[string]any) { m["assets"].([]any)[1].(map[string]any)["size"] = maxSums + 1 },
		"zero size":         func(m map[string]any) { m["assets"].([]any)[0].(map[string]any)["size"] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			routes := releaseRoutes(t, "hub", "amd64", validArchive(t, "hub"))
			var m map[string]any
			if err := json.Unmarshal(routes[officialAPI+"/tags/v0.3.0"], &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			body, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			routes[officialAPI+"/tags/v0.3.0"] = body
			_, err = testSource(t, routes).Download(context.Background(), "hub", "amd64", "v0.3.0")
			if err == nil {
				t.Fatal("accepted invalid metadata")
			}
		})
	}
	for _, body := range []string{`{`, `{"tag_name":"v0.3.0","draft":true}`, `{"tag_name":"v0.3.0","prerelease":true}`, `{"tag_name":"v0.3.0-rc1"}`} {
		if _, err := testSource(t, map[string][]byte{officialAPI + "/latest": []byte(body)}).Latest(context.Background()); err == nil {
			t.Errorf("accepted latest %s", body)
		}
	}
}

func TestOfficialChecksum(t *testing.T) {
	asset := "heron-hub_linux_amd64.tar.gz"
	valid := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("archive")), asset)
	for name, sums := range map[string]string{
		"missing": "", "wrong asset": strings.ReplaceAll(valid, asset, "other.tar.gz"), "duplicate": valid + valid,
		"bad digest": "g" + valid[1:], "short digest": valid[1:], "bad separator": valid[:64] + "xx" + valid[66:],
		"path": strings.ReplaceAll(valid, asset, "../"+asset), "backslash": strings.ReplaceAll(valid, asset, "a\\b"), "empty name": valid[:66],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := checksum([]byte(sums), asset); err == nil {
				t.Fatal("accepted invalid checksum")
			}
		})
	}
	for _, sums := range []string{valid, strings.Replace(valid, "  ", " *", 1), strings.TrimSuffix(valid, "\n")} {
		got, err := checksum([]byte(sums), asset)
		if err != nil || got != sha256.Sum256([]byte("archive")) {
			t.Fatalf("checksum = %x, %v", got, err)
		}
	}
	routes := releaseRoutes(t, "hub", "amd64", validArchive(t, "hub"))
	routes[officialDownloads+"v0.3.0/SHA256SUMS"] = []byte(valid)
	if _, err := testSource(t, routes).Download(context.Background(), "hub", "amd64", "v0.3.0"); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestOfficialArchiveStructure(t *testing.T) {
	for name, h := range map[string]*tar.Header{
		"parent path": {Name: "../heron-hub", Typeflag: tar.TypeReg}, "absolute": {Name: "/heron-hub", Typeflag: tar.TypeReg},
		"symlink": {Name: "heron-hub", Typeflag: tar.TypeSymlink, Linkname: "target"}, "hardlink": {Name: "heron-hub", Typeflag: tar.TypeLink, Linkname: "target"},
		"directory": {Name: "heron-hub", Typeflag: tar.TypeDir}, "other binary": {Name: "heron-agent", Typeflag: tar.TypeReg},
		"unknown": {Name: "README", Typeflag: tar.TypeReg}, "empty": {Name: "heron-hub", Typeflag: tar.TypeReg},
		"pax": {Name: "heron-hub", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"comment": "unexpected"}},
	} {
		t.Run(name, func(t *testing.T) {
			archive := makeArchive(t, []*tar.Header{h}, []string{""})
			if _, err := extractBinary(archive, "hub"); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		h := &tar.Header{Name: "heron-hub", Size: 1, Typeflag: tar.TypeReg}
		archive := makeArchive(t, []*tar.Header{h, h}, []string{"a", "b"})
		if _, err := extractBinary(archive, "hub"); err == nil {
			t.Fatal("accepted duplicate binary")
		}
	})
	t.Run("missing", func(t *testing.T) {
		archive := makeArchive(t, []*tar.Header{{Name: "heron-hub.service", Size: 1, Typeflag: tar.TypeReg}}, []string{"a"})
		if _, err := extractBinary(archive, "hub"); err == nil {
			t.Fatal("accepted missing binary")
		}
	})
	t.Run("agent openrc", func(t *testing.T) {
		archive := makeArchive(t, []*tar.Header{{Name: "heron-agent", Size: 1, Typeflag: tar.TypeReg}, {Name: "heron-agent.openrc", Size: 1, Typeflag: tar.TypeReg}}, []string{"a", "b"})
		if body, err := extractBinary(archive, "agent"); err != nil || string(body) != "a" {
			t.Fatalf("agent archive = %q, %v", body, err)
		}
	})
	archive := validArchive(t, "hub")
	corrupt := bytes.Clone(archive)
	corrupt[len(corrupt)-8] ^= 0xff
	for name, body := range map[string][]byte{"truncated": archive[:len(archive)-1], "gzip checksum": corrupt, "trailing": append(bytes.Clone(archive), 'x'), "multistream": append(bytes.Clone(archive), archive...), "not gzip": []byte("binary")} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractBinary(body, "hub"); err == nil {
				t.Fatal("accepted damaged archive")
			}
		})
	}
}

func TestOfficialArchiveSizeLimits(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "heron-hub", Typeflag: tar.TypeReg, Size: maxBinary + 1},
		{Name: "heron-hub.service", Typeflag: tar.TypeReg, Size: maxSums + 1},
	} {
		t.Run(h.Name, func(t *testing.T) {
			var buffer bytes.Buffer
			gz := gzip.NewWriter(&buffer)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			// 只写合法的大尺寸头；尺寸准入应早于正文读取，不需要分配数百 MiB。
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := extractBinary(buffer.Bytes(), "hub"); err == nil || !strings.Contains(err.Error(), "entry size") {
				t.Fatalf("oversized header error = %v", err)
			}
		})
	}
	archive := makeArchive(t, []*tar.Header{{Name: "heron-hub", Typeflag: tar.TypeReg, Size: 1, PAXRecords: map[string]string{"comment": "unexpected"}}}, []string{"a"})
	if _, err := extractBinary(archive, "hub"); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("PAX error = %v", err)
	}
}

func TestOfficialHTTPBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		length int64
		body   string
		err    error
		want   string
	}{
		{"status", 404, 0, "", nil, "HTTP 404"}, {"declared size", 200, maxMetadata + 1, "", nil, "size limit"},
		{"actual size", 200, -1, strings.Repeat("x", maxMetadata+1), nil, "size limit"}, {"network", 0, 0, "", context.DeadlineExceeded, "deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &OfficialSource{http: githubtransport.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: r}, nil
			}), time.Second)}
			if _, err := s.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
	for _, destination := range []string{"http://github.com/a", "https://127.0.0.1/a", "https://example.com/a", "https://github.com:444/a", "https://user:pass@github.com/a"} {
		t.Run(destination, func(t *testing.T) {
			calls := 0
			s := &OfficialSource{http: githubtransport.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{destination}}, Request: r}, nil
			}), time.Second)}
			if _, err := s.Latest(context.Background()); err == nil || calls != 1 {
				t.Fatalf("redirect err=%v, calls=%d", err, calls)
			}
		})
	}
}
