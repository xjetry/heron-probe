package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/githubtransport"
	"github.com/xjetry/heron-probe/internal/releasesig"
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
	return makeArchive(t, []*tar.Header{{Name: "heron-" + role, Mode: 0o755, Size: 6, Typeflag: tar.TypeReg}, {Name: "heron-" + role + ".service", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg}}, []string{"binary", "unit"})
}

// releaseRoutes 只登记三条下载地址，内容用测试私钥现场签名；任何 releases API 请求都未登记，
// testSource 会让用例失败，钉住取回不再查询草稿、预发布与资产清单。
func releaseRoutes(t *testing.T, role, arch, version string, archive []byte) (map[string][]byte, Artifacts) {
	t.Helper()
	asset, err := archiveName(role, arch)
	if err != nil {
		t.Fatal(err)
	}
	a := signedArtifacts(role, arch, version, archive)
	return map[string][]byte{
		officialDownloads + version + "/SHA256SUMS":     a.Sums,
		officialDownloads + version + "/SHA256SUMS.sig": a.Signature,
		officialDownloads + version + "/" + asset:       a.Archive,
	}, a
}

func TestOfficialFetchUsesOnlyDownloadURLs(t *testing.T) {
	for _, pair := range [][2]string{{"hub", "amd64"}, {"hub", "arm64"}, {"agent", "amd64"}, {"agent", "arm64"}, {"agent", "armv7"}, {"agent", "386"}, {"agent", "riscv64"}} {
		t.Run(strings.Join(pair[:], "/"), func(t *testing.T) {
			routes, want := releaseRoutes(t, pair[0], pair[1], "v0.3.0", validArchive(t, pair[0]))
			a, err := testSource(t, routes).Fetch(context.Background(), Request{Version: "v0.3.0"}, pair[0], pair[1])
			if err != nil {
				t.Fatal(err)
			}
			if string(a.Sums) != string(want.Sums) || string(a.Signature) != string(want.Signature) || string(a.Archive) != string(want.Archive) {
				t.Fatal("fetched bytes differ from the registered downloads")
			}
			if bin, err := Accept(testKeys(), pair[0], pair[1], "v0.3.0", a); err != nil || string(bin) != "binary" {
				t.Fatalf("Accept = %q, %v", bin, err)
			}
		})
	}
}

func TestOfficialRejectsInputBeforeNetwork(t *testing.T) {
	s := testSource(t, nil)
	for _, args := range [][3]string{{"hub", "386", "v0.3.0"}, {"agent", "mips", "v0.3.0"}, {"updater", "amd64", "v0.3.0"}, {"hub", "amd64", "../../other"}, {"hub", "amd64", "v0.3.0-rc1"}, {"hub", "amd64", "v00.3.0"}} {
		if _, err := s.Fetch(context.Background(), Request{Version: args[2]}, args[0], args[1]); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestOfficialRejectsReleaseMetadata(t *testing.T) {
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
	// 摘要核对在验签之后：已签清单里的目标条目与实际归档不符时，整份产物被拒。
	a := signedArtifacts("hub", "amd64", "v0.3.0", makeArchive(t, []*tar.Header{{Name: "heron-hub", Mode: 0o755, Size: 7, Typeflag: tar.TypeReg}}, []string{"archive"}))
	a.Archive = makeArchive(t, []*tar.Header{{Name: "heron-hub", Mode: 0o755, Size: 9, Typeflag: tar.TypeReg}}, []string{"different"})
	if _, err := Accept(testKeys(), "hub", "amd64", "v0.3.0", a); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestOfficialArchiveStructure(t *testing.T) {
	// 结构错误必须连同合法签名一起被拒：签名只证明字节未变，不证明结构安全。
	reject := func(t *testing.T, name string, archive []byte) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if _, err := Accept(testKeys(), "hub", "amd64", "v0.3.0", signedArtifacts("hub", "amd64", "v0.3.0", archive)); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	for name, h := range map[string]*tar.Header{
		"parent path": {Name: "../heron-hub", Typeflag: tar.TypeReg}, "absolute": {Name: "/heron-hub", Typeflag: tar.TypeReg},
		"symlink": {Name: "heron-hub", Typeflag: tar.TypeSymlink, Linkname: "target"}, "hardlink": {Name: "heron-hub", Typeflag: tar.TypeLink, Linkname: "target"},
		"directory": {Name: "heron-hub", Typeflag: tar.TypeDir}, "other binary": {Name: "heron-agent", Typeflag: tar.TypeReg},
		"unknown": {Name: "README", Typeflag: tar.TypeReg}, "empty": {Name: "heron-hub", Typeflag: tar.TypeReg},
		"pax": {Name: "heron-hub", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"comment": "unexpected"}},
	} {
		reject(t, name, makeArchive(t, []*tar.Header{h}, []string{""}))
	}
	binary := &tar.Header{Name: "heron-hub", Size: 1, Typeflag: tar.TypeReg}
	reject(t, "duplicate", makeArchive(t, []*tar.Header{binary, binary}, []string{"a", "b"}))
	reject(t, "missing", makeArchive(t, []*tar.Header{{Name: "heron-hub.service", Size: 1, Typeflag: tar.TypeReg}}, []string{"a"}))
	t.Run("agent openrc", func(t *testing.T) {
		archive := makeArchive(t, []*tar.Header{{Name: "heron-agent", Size: 1, Typeflag: tar.TypeReg}, {Name: "heron-agent.openrc", Size: 1, Typeflag: tar.TypeReg}}, []string{"a", "b"})
		if body, err := Accept(testKeys(), "agent", "amd64", "v0.3.0", signedArtifacts("agent", "amd64", "v0.3.0", archive)); err != nil || string(body) != "a" {
			t.Fatalf("agent archive = %q, %v", body, err)
		}
	})
	archive := validArchive(t, "hub")
	corrupt := bytes.Clone(archive)
	corrupt[len(corrupt)-8] ^= 0xff
	for name, body := range map[string][]byte{"truncated": archive[:len(archive)-1], "gzip checksum": corrupt, "trailing": append(bytes.Clone(archive), 'x'), "multistream": append(bytes.Clone(archive), archive...), "not gzip": []byte("binary")} {
		reject(t, name, body)
	}
}

func TestOfficialArchiveSizeLimits(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "heron-hub", Typeflag: tar.TypeReg, Size: maxBinary + 1},
		{Name: "heron-hub.service", Typeflag: tar.TypeReg, Size: maxServiceFile + 1},
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
			if _, err := Accept(testKeys(), "hub", "amd64", "v0.3.0", signedArtifacts("hub", "amd64", "v0.3.0", buffer.Bytes())); err == nil || !strings.Contains(err.Error(), "entry size") {
				t.Fatalf("oversized header error = %v", err)
			}
		})
	}
	archive := makeArchive(t, []*tar.Header{{Name: "heron-hub", Typeflag: tar.TypeReg, Size: 1, PAXRecords: map[string]string{"comment": "unexpected"}}}, []string{"a"})
	if _, err := Accept(testKeys(), "hub", "amd64", "v0.3.0", signedArtifacts("hub", "amd64", "v0.3.0", archive)); err == nil || !strings.Contains(err.Error(), "non-regular") {
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
		{"status", 404, 0, "", nil, "HTTP 404"}, {"declared size", 200, releasesig.MaxSums + 1, "", nil, "size limit"},
		{"actual size", 200, -1, strings.Repeat("x", releasesig.MaxSums+1), nil, "size limit"}, {"network", 0, 0, "", context.DeadlineExceeded, "deadline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &OfficialSource{http: githubtransport.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: r}, nil
			}), time.Second)}
			if _, err := s.Fetch(context.Background(), Request{Version: "v0.3.0"}, "hub", "amd64"); err == nil || !strings.Contains(err.Error(), tc.want) {
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
			if _, err := s.Fetch(context.Background(), Request{Version: "v0.3.0"}, "hub", "amd64"); err == nil || calls != 1 {
				t.Fatalf("redirect err=%v, calls=%d", err, calls)
			}
		})
	}
}
