package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func clientFor(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: endpoint, Bucket: "backups", Region: "auto", AccessKey: "access", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC) }
	return c
}

func TestObjectOperations(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		t.Run(fmt.Sprint(virtual), func(t *testing.T) {
			var methods []string
			key := "config/a//../sp ace+雪%2F.db"
			payload := []byte("a real database body")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				wantPath, wantHost := "/base/backups/"+key, "s3.example"
				if virtual {
					wantPath, wantHost = "/base/"+key, "backups.s3.example"
				}
				if r.URL.Path != wantPath || r.Host != wantHost {
					t.Errorf("object address = %s %s, want %s %s", r.Host, r.URL.Path, wantHost, wantPath)
				}
				if got := r.Header.Get("X-Amz-Date"); got != "20260928T010203Z" {
					t.Errorf("x-amz-date = %q", got)
				}
				wantHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
				if r.Method == "PUT" {
					wantHash = fmt.Sprintf("%x", sha256.Sum256(payload))
				}
				if got := r.Header.Get("X-Amz-Content-Sha256"); got != wantHash {
					t.Errorf("payload hash = %q, want %q", got, wantHash)
				}
				if !regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=access/20260928/auto/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=[0-9a-f]{64}$`).MatchString(r.Header.Get("Authorization")) {
					t.Errorf("authorization structure = %q", r.Header.Get("Authorization"))
				}
				if r.Method == "PUT" {
					got, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(got, payload) {
						t.Errorf("uploaded body = %q, %v", got, err)
					}
				}
				if r.Method == "GET" {
					_, _ = w.Write(payload)
				} else {
					w.WriteHeader(204)
				}
			}))
			defer srv.Close()
			c := clientFor(t, "http://s3.example/base")
			c.cfg.VirtualHost = virtual
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = nil
			tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			}
			defer tr.CloseIdleConnections()
			c.http.Transport = tr
			body := bytes.NewReader(append([]byte("skip"), payload...))
			_, _ = body.Seek(4, io.SeekStart)
			if err := c.PutObject(t.Context(), key, body); err != nil {
				t.Fatal(err)
			}
			var dst bytes.Buffer
			if err := c.GetObject(t.Context(), key, &dst, int64(len(payload))); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(dst.Bytes(), payload) {
				t.Errorf("download = %q, want %q", dst.Bytes(), payload)
			}
			if err := c.DeleteObject(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(methods, ","); got != "PUT,GET,DELETE" {
				t.Fatalf("methods = %q", got)
			}
		})
	}
}

func TestListPagination(t *testing.T) {
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("prefix") != "a +/" || q.Get("list-type") != "2" || q.Get("max-keys") != "10" || q.Get("encoding-type") != "url" {
			t.Errorf("list query = %v", q)
		}
		token := q.Get("continuation-token")
		tokens = append(tokens, token)
		if token == "" {
			fmt.Fprint(w, `<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>true</IsTruncated><NextContinuationToken>a+/=</NextContinuationToken><Contents><Key>a%20%2B%2Fone</Key><Size>7</Size><LastModified>2026-09-28T01:02:03Z</LastModified></Contents></ListBucketResult>`)
		} else {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>a +/two</Key><Size>8</Size></Contents></ListBucketResult>`)
		}
	}))
	defer srv.Close()
	objects, err := clientFor(t, srv.URL).ListObjectsV2(t.Context(), "a +/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Key != "a +/one" || objects[1].Key != "a +/two" || objects[0].Size != 7 || objects[0].LastModified.Format(time.RFC3339) != "2026-09-28T01:02:03Z" {
		t.Errorf("joined pages = %+v", objects)
	}
	if strings.Join(tokens, ",") != ",a+/=" {
		t.Fatalf("continuation tokens = %q", tokens)
	}
}

func TestHTTPFailuresAndNoRedirect(t *testing.T) {
	for _, status := range []int{301, 403, 404, 408, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			targetHits := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits++ }))
			defer target.Close()
			body := strings.Repeat("错", 100)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			err := clientFor(t, srv.URL).DeleteObject(t.Context(), "credential-in-path")
			if targetHits != 0 {
				t.Errorf("redirect leaked request: %d target hits", targetHits)
			}
			var e *Error
			if !errors.As(err, &e) || e.Kind != "http_status" || e.StatusCode != status || e.Detail != body[:200] {
				t.Fatalf("HTTP classification = %#v", err)
			}
			wantRetry := status == 408 || status == 429 || status >= 500
			if e.Retryable() != wantRetry {
				t.Fatalf("retryable HTTP %d = %v", status, e.Retryable())
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := clientFor(t, "http://127.0.0.1:1").DeleteObject(ctx, "credential-in-path")
	var e *Error
	if !errors.As(err, &e) || e.Kind != "transport" || !e.Retryable() || strings.Contains(err.Error(), "credential-in-path") || !errors.Is(err, context.Canceled) {
		t.Fatalf("transport classification = %v", err)
	}
}

func TestResponseBounds(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		list       bool
	}{
		{"oversized list", strings.Repeat("x", responseLimit+1), true},
		{"invalid XML", "<not-list/>", true},
		{"missing token", "<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>", true},
		{"repeated token", "<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>same</NextContinuationToken></ListBucketResult>", true},
		{"oversized object", "too much data", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			c := clientFor(t, srv.URL)
			var err error
			if tc.list {
				_, err = c.ListObjectsV2(t.Context(), "")
			} else {
				err = c.GetObject(t.Context(), "object", io.Discard, 3)
			}
			var e *Error
			if !errors.As(err, &e) || e.Kind != "response" {
				t.Fatalf("invalid response accepted: %v", err)
			}
		})
	}
}

func TestConfigurationAndDisabled(t *testing.T) {
	full := Config{Endpoint: "https://s3.example", Bucket: "backups", Region: "auto", AccessKey: "access", Secret: "secret"}
	if !full.Enabled() {
		t.Fatal("complete backup disabled")
	}
	for _, field := range []string{"endpoint", "bucket", "access key", "secret"} {
		c := full
		switch field {
		case "endpoint":
			c.Endpoint = ""
		case "bucket":
			c.Bucket = ""
		case "access key":
			c.AccessKey = ""
		case "secret":
			c.Secret = ""
		}
		if c.Enabled() {
			t.Errorf("backup enabled without %s", field)
		}
		if _, err := New(c); err == nil {
			t.Errorf("client constructed without %s", field)
		}
	}
	for _, endpoint := range []string{"ftp://host", "https://user:pass@host", "https://host?a=b", "https://host/#secret", "not a url"} {
		c := full
		c.Endpoint = endpoint
		if _, err := New(c); err == nil {
			t.Errorf("invalid endpoint accepted: %s", endpoint)
		}
	}
}
