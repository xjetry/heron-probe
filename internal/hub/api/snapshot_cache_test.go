package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/testwait"
)

// observed 是一次响应里缓存必须与直连 connect 一致的部分；gzip 正文先解开再比，压缩字节本身不是契约。
type observed struct {
	status                             int
	contentType, contentEncoding, vary string
	body                               string
}

func observe(t *testing.T, h http.Handler, newReq func() *http.Request) observed {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq())
	resp := rec.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	return observed{resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Encoding"), resp.Header.Get("Vary"), string(body)}
}

func snapshotRequest(method, query, body string, header map[string]string) func() *http.Request {
	return func() *http.Request {
		target := probev1connect.PublicServiceGetSnapshotProcedure
		if query != "" {
			target += "?" + query
		}
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		// 值里的换行拆成同名头的多行：有的形态要测多行同名头。
		for k, v := range header {
			for _, line := range strings.Split(v, "\n") {
				req.Header.Add(k, line)
			}
		}
		return req
	}
}

// 缓存对每一种请求形态的应答都与直连 connect 相同：规范形态由缓存回答（第二次是命中），
// 其余形态原样交给 connect；缓存不替 connect 接受它会拒绝的请求，也不给请求方它没要的压缩。
func TestSnapshotCacheIsTransparent(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "pub")
	h.setPublic(t, id, "pub", true)
	_, direct := h.pub.connectHandler()
	const json = "connect=v1&encoding=json&message=%7B%7D"
	jsonCT := map[string]string{"Content-Type": "application/json"}
	acceptGzip := map[string]string{"Accept-Encoding": "gzip"}
	primes := []func() *http.Request{
		snapshotRequest(http.MethodGet, json, "", nil),
		snapshotRequest(http.MethodGet, json, "", acceptGzip),
		snapshotRequest(http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil),
		snapshotRequest(http.MethodPost, "", "{}", jsonCT),
		snapshotRequest(http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Accept-Encoding": "gzip"}),
		snapshotRequest(http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}),
	}
	// hit 标出规范形态：键已有缓存时两次都不进 connect。其余形态每次都交给 connect。
	for _, c := range []struct {
		name, method, query, body string
		header                    map[string]string
		hit                       bool
	}{
		{"json", http.MethodGet, json, "", nil, true},
		{"json without connect", http.MethodGet, "encoding=json&message=%7B%7D", "", nil, true},
		{"json gzip", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "gzip"}, true},
		{"json br then gzip", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "br, gzip"}, true},
		{"json gzip q=0", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "gzip;q=0"}, true},
		{"json identity line then gzip line", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "identity\ngzip"}, true},
		{"json base64 unpadded", http.MethodGet, "connect=v1&encoding=json&base64=1&message=e30", "", nil, false},
		{"json base64 padded", http.MethodGet, "connect=v1&encoding=json&base64=1&message=e30%3D", "", nil, false},
		{"json base64 flag with literal", http.MethodGet, "connect=v1&encoding=json&base64=1&message=%7B%7D", "", nil, false},
		{"json base64=0", http.MethodGet, "connect=v1&encoding=json&base64=0&message=%7B%7D", "", nil, false},
		{"json whitespace", http.MethodGet, "connect=v1&encoding=json&message=%7B%20%7D", "", nil, false},
		{"json unknown field", http.MethodGet, "connect=v1&encoding=json&message=%7B%22x%22%3A1%7D", "", nil, false},
		{"json without message", http.MethodGet, "connect=v1&encoding=json", "", nil, false},
		{"proto base64", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil, true},
		{"proto raw", http.MethodGet, "connect=v1&encoding=proto&message=", "", nil, true},
		{"proto unknown field", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=CAE", "", nil, false},
		{"proto invalid", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=AA", "", nil, false},
		{"duplicate encoding", http.MethodGet, "connect=v1&encoding=json&encoding=proto&message=%7B%7D", "", nil, false},
		{"connect v2", http.MethodGet, "connect=v2&encoding=json&message=%7B%7D", "", nil, false},
		{"compressed request", http.MethodGet, "connect=v1&encoding=json&compression=gzip&message=%7B%7D", "", nil, false},
		{"timeout header", http.MethodGet, json, "", map[string]string{"Connect-Timeout-Ms": "abc"}, false},
		{"post json", http.MethodPost, "", "{}", jsonCT, true},
		{"post json gzip", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Accept-Encoding": "gzip"}, true},
		{"post proto", http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}, true},
		{"post json whitespace", http.MethodPost, "", "{ }", jsonCT, false},
		{"post json unknown field", http.MethodPost, "", `{"x":1}`, jsonCT, false},
		{"post json charset", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json; charset=utf-8"}, false},
		{"post protocol version 2", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Connect-Protocol-Version": "2"}, false},
		{"post text", http.MethodPost, "", "{}", map[string]string{"Content-Type": "text/plain"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			cached := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				direct.ServeHTTP(w, r)
			}), h.clk)
			// 先让每个键都有缓存：错把某种形态当成规范形态的缺陷，只在它的键已有缓存时才会下发错的字节。
			for _, prime := range primes {
				observe(t, cached, prime)
			}
			calls = 0
			req := snapshotRequest(c.method, c.query, c.body, c.header)
			want := observe(t, direct, req)
			for i := range 2 {
				if got := observe(t, cached, req); got != want {
					t.Fatalf("call %d through the cache:\n got %+v\nwant %+v", i+1, got, want)
				}
			}
			if wantCalls := map[bool]int{true: 0, false: 2}[c.hit]; calls != wantCalls {
				t.Fatalf("connect ran %d times for two requests, want %d", calls, wantCalls)
			}
		})
	}
}

// 同一个键在窗口内只进一次处理器：并发的同键请求排队，拿到同一份字节。
func TestSnapshotCacheSerializesOnceForConcurrentRequests(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int64
	allArrived := make(chan struct{})
	cache := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		<-allArrived
		fmt.Fprintf(w, "serialization %d", n)
	}), clk)
	const n = 16
	var arrived atomic.Int64
	bodies := make(chan string, n)
	for range n {
		go func() {
			arrived.Add(1)
			rec := httptest.NewRecorder()
			cache.ServeHTTP(rec, snapshotRequest(http.MethodGet, "connect=v1&encoding=json&message=%7B%7D", "", nil)())
			bodies <- rec.Body.String()
		}()
	}
	testwait.Until(t, time.Millisecond, func() bool { return arrived.Load() == n }, "only %v of %d requests arrived",
		testwait.When(func() string { return fmt.Sprint(arrived.Load()) }), n)
	close(allArrived)
	for range n {
		select {
		case b := <-bodies:
			if b != "serialization 1" {
				t.Errorf("body %q, want the single serialization", b)
			}
		case <-time.After(testwait.Bound):
			t.Fatal("a request did not finish")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times for one key within one window", got)
	}
}

func TestSnapshotCacheKeysWindowAndWhatItStores(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var calls int
	gzipHonest, fail := true, false
	cache := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if gzipHonest && negotiatedCompression(r.Header.Get("Accept-Encoding")) == "gzip" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		fmt.Fprintf(w, "serialization %d", calls)
	}), clk)
	serve := func(method, query, body string, header map[string]string) string {
		rec := httptest.NewRecorder()
		cache.ServeHTTP(rec, snapshotRequest(method, query, body, header)())
		return rec.Body.String()
	}
	const json = "connect=v1&encoding=json&message=%7B%7D"
	acceptGzip := map[string]string{"Accept-Encoding": "gzip"}
	for _, s := range []struct {
		name, method, query, body string
		header                    map[string]string
		want                      string
	}{
		{"json GET", http.MethodGet, json, "", nil, "serialization 1"},
		{"json GET again", http.MethodGet, json, "", nil, "serialization 1"},
		{"proto GET", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil, "serialization 2"},
		{"proto GET without base64 flag", http.MethodGet, "encoding=proto&message=", "", nil, "serialization 2"},
		{"gzip GET", http.MethodGet, json, "", acceptGzip, "serialization 3"},
		{"json POST", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json"}, "serialization 4"},
		{"proto POST", http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}, "serialization 5"},
	} {
		if got := serve(s.method, s.query, s.body, s.header); got != s.want {
			t.Fatalf("%s: %q, want %q", s.name, got, s.want)
		}
	}
	// 窗口是 spec §10 的字面值 1 秒，不引用 snapshotTTL：常量改了，这里要红。
	clk.Advance(time.Second - time.Nanosecond)
	if got := serve(http.MethodGet, json, "", nil); got != "serialization 1" {
		t.Fatalf("expired before one second: %q", got)
	}
	clk.Advance(time.Nanosecond)
	if got := serve(http.MethodGet, json, "", nil); got != "serialization 6" {
		t.Fatalf("served after one second: %q", got)
	}
	// connect 实际的压缩与键不符时不入缓存：下一次仍进处理器。
	gzipHonest = false
	clk.Advance(time.Second)
	for _, want := range []string{"serialization 7", "serialization 8"} {
		if got := serve(http.MethodGet, json, "", acceptGzip); got != want {
			t.Fatalf("mismatched compression was cached: %q, want %q", got, want)
		}
	}
	// 失败的响应不入缓存。
	fail = true
	for _, want := range []string{"serialization 9", "serialization 10"} {
		if got := serve(http.MethodGet, "connect=v1&encoding=proto&message=", "", nil); got != want {
			t.Fatalf("failed response was cached: %q, want %q", got, want)
		}
	}
}

// 节点改为非公开之后：已缓存的快照最多再下发 1 秒（spec §10 的字面值），之后不再出现；
// 历史查询不经这层缓存，立即 NotFound；此前没有缓存的键（POST）立即反映改动。
func TestSnapshotCacheWindowAfterNodeTurnsPrivate(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "flip")
	h.setPublic(t, id, "flip", true)
	snapshot := jsonQuery("{}")
	before := pubGet(t, h, "GetSnapshot", snapshot, nil)
	if before.status != http.StatusOK || !bytes.Contains(before.body, []byte(`"flip"`)) {
		t.Fatalf("public node missing: %d %s", before.status, before.body)
	}
	h.setPublic(t, id, "flip", false)
	if within := pubGet(t, h, "GetSnapshot", snapshot, nil); !bytes.Equal(within.body, before.body) {
		t.Fatalf("snapshot re-serialized within the window: %s", within.body)
	}
	window := fmt.Sprintf(`{"nodeId":"%d","from":"0","to":"3600"}`, id)
	if got := pubGet(t, h, "QueryMetrics", jsonQuery(window), nil); got.status != http.StatusNotFound {
		t.Fatalf("history of a private node: %d %s", got.status, got.body)
	}
	if got := pubPost(t, h, "GetSnapshot", "{}", nil); bytes.Contains(got.body, []byte(`"flip"`)) {
		t.Fatalf("uncached key still lists the private node: %s", got.body)
	}
	h.clk.Advance(time.Second)
	if after := pubGet(t, h, "GetSnapshot", snapshot, nil); after.status != http.StatusOK || bytes.Contains(after.body, []byte(`"flip"`)) {
		t.Fatalf("private node still served after the window: %d %s", after.status, after.body)
	}
}

// 快照缓存的命中同样计数：限流包在缓存外面。
func TestPublicRateLimitCountsSnapshotCacheHits(t *testing.T) {
	h := newHarness(t, "")
	for i := range 60 {
		if got := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil); got.status != http.StatusOK {
			t.Fatalf("request %d: %d %s", i+1, got.status, got.body)
		}
	}
	if got := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil); got.status != http.StatusTooManyRequests {
		t.Fatalf("cache hit past the burst: %d", got.status)
	}
}
