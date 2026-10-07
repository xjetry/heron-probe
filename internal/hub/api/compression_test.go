package api

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/theme/themetest"
)

// 直接读取线上字节，不经 Connect 客户端解压；gRPC 的压缩位在消息帧里，只有检查 HTTP 头会漏掉它。
func TestAdminResponseCompressionPolicy(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	h.createNode(t, strings.Repeat("node-", 10))
	srv := httptest.NewUnstartedServer(h.srv.Config.Handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()
	client.Transport.(*http.Transport).DisableCompression = true
	loginURL, err := http.NewRequest(http.MethodGet, h.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookies := h.http.Jar.Cookies(loginURL.URL)
	for _, protocol := range []string{"connect", "grpc", "grpc-web"} {
		for _, compressedRequest := range []bool{false, true} {
			requestEncoding := "identity-request"
			if compressedRequest {
				requestEncoding = "gzip-request"
			}
			for _, tc := range []struct {
				name, method string
				request      proto.Message
				response     proto.Message
				anonymous    bool
			}{
				{"list", "ListNodes", &heronv1.ListNodesRequest{}, &heronv1.ListNodesResponse{}, false},
				{"upload", "UploadTheme", &heronv1.UploadThemeRequest{Package: themetest.Minimal(t, "compression")}, &heronv1.UploadThemeResponse{}, false},
				{"unauthenticated", "ListNodes", &heronv1.ListNodesRequest{}, nil, true},
			} {
				t.Run(protocol+"/"+requestEncoding+"/"+tc.name, func(t *testing.T) {
					var body []byte
					var err error
					if protocol == "connect" {
						body, err = protojson.Marshal(tc.request)
					} else {
						body, err = proto.Marshal(tc.request)
					}
					if err != nil {
						t.Fatal(err)
					}
					if compressedRequest {
						var buf bytes.Buffer
						zw := gzip.NewWriter(&buf)
						if _, err := zw.Write(body); err != nil {
							t.Fatal(err)
						}
						if err := zw.Close(); err != nil {
							t.Fatal(err)
						}
						body = buf.Bytes()
					}
					if protocol != "connect" {
						frame := make([]byte, 5+len(body))
						if compressedRequest {
							frame[0] = 1
						}
						binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)))
						copy(frame[5:], body)
						body = frame
					}
					req, err := http.NewRequest(http.MethodPost, srv.URL+"/heron.v1.AdminService/"+tc.method, bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
					if protocol == "connect" {
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Connect-Protocol-Version", "1")
						if compressedRequest {
							req.Header.Set("Content-Encoding", "gzip")
						}
					} else {
						req.Header.Set("Content-Type", "application/"+protocol+"+proto")
						req.Header.Set("Grpc-Accept-Encoding", "gzip")
						req.Header.Set("TE", "trailers")
						if compressedRequest {
							req.Header.Set("Grpc-Encoding", "gzip")
						}
					}
					if !tc.anonymous {
						for _, cookie := range cookies {
							req.AddCookie(cookie)
						}
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					wire, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					if got := resp.Header.Get("Cache-Control"); got != "no-store, no-transform" {
						t.Errorf("Cache-Control = %q, want no-store, no-transform", got)
					}
					if got := resp.Header.Get("Content-Encoding"); got != "" {
						t.Errorf("Content-Encoding = %q, want uncompressed response", got)
					}
					if protocol == "connect" {
						want := http.StatusOK
						if tc.anonymous {
							want = http.StatusUnauthorized
						}
						if resp.StatusCode != want {
							t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, wire)
						}
						if tc.response != nil {
							if err := protojson.Unmarshal(wire, tc.response); err != nil {
								t.Errorf("identity JSON response: %v", err)
							}
						}
						return
					}
					if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK {
						t.Fatalf("gRPC transport = %s %d", resp.Proto, resp.StatusCode)
					}
					status := resp.Trailer.Get("Grpc-Status")
					if status == "" {
						status = resp.Header.Get("Grpc-Status")
					}
					messages := 0
					for len(wire) > 0 {
						if len(wire) < 5 || uint64(binary.BigEndian.Uint32(wire[1:5])) > uint64(len(wire)-5) {
							t.Fatalf("invalid gRPC frame: %x", wire)
						}
						flags, n := wire[0], int(binary.BigEndian.Uint32(wire[1:5]))
						payload := wire[5 : 5+n]
						wire = wire[5+n:]
						if flags&1 != 0 {
							t.Errorf("gRPC response frame is compressed: flags=%#x", flags)
						}
						if flags&0x80 != 0 {
							for _, line := range strings.Split(string(payload), "\r\n") {
								if key, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(key, "grpc-status") {
									status = strings.TrimSpace(value)
								}
							}
						} else {
							messages++
							if tc.response != nil {
								if err := proto.Unmarshal(payload, tc.response); err != nil {
									t.Errorf("identity protobuf response: %v", err)
								}
							}
						}
					}
					wantStatus, wantMessages := "0", 1
					if tc.anonymous {
						wantStatus, wantMessages = "16", 0
					}
					if status != wantStatus || messages != wantMessages {
						t.Errorf("gRPC status/messages = %q/%d, want %q/%d", status, messages, wantStatus, wantMessages)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name, body, origin string
		want               int
	}{
		{"origin-rejected", "{}", "https://other.invalid", http.StatusForbidden},
		{"decode-rejected", "{", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/heron.v1.AdminService/ListNodes", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store, no-transform" {
				t.Errorf("Cache-Control = %q, want no-store, no-transform", got)
			}
			if got := resp.Header.Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want uncompressed response", got)
			}
		})
	}
	t.Run("public-control", func(t *testing.T) {
		got := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd"})
		if got.status != http.StatusOK || got.header.Get("Content-Encoding") != "gzip" || got.header.Get("Cache-Control") != "max-age=1" {
			t.Fatalf("public status/encoding/cache = %d/%q/%q", got.status, got.header.Get("Content-Encoding"), got.header.Get("Cache-Control"))
		}
	})
}
