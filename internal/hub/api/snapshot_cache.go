package api

import (
	"bytes"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
)

// snapshotTTL 是 GetSnapshot 响应字节的缓存窗口（§10：1 秒）。访客再多，hub 每个键每个窗口最多序列化一次；
// 代价是节点改为非公开后，已缓存的快照最多再下发 snapshotTTL。历史查询不经这层缓存。
const snapshotTTL = time.Second

// snapshotKey 区分响应字节会不同的请求：GET 与 POST 的响应头不同（connect 给 GET 加 Vary），codec 决定正文编码，
// 压缩由 Accept-Encoding 协商。规范形态的请求消息为空，不进键。
type snapshotKey struct {
	get         bool
	codec       string // "json" 或 "proto"
	compression string // "identity" 或 "gzip"
}

type snapshotEntry struct {
	// mu 让同键的并发请求排队：窗口内只有第一个进到 connect 处理器，其余拿它存下的字节。
	mu      sync.Mutex
	filled  bool
	expires time.Duration
	status  int
	header  http.Header
	body    []byte
}

type snapshotCache struct {
	next http.Handler
	clk  clock.Clock

	mu      sync.Mutex // 只保护 entries 这张表；条目内容由各自的 mu 保护
	entries map[snapshotKey]*snapshotEntry
}

func newSnapshotCache(next http.Handler, clk clock.Clock) http.Handler {
	return &snapshotCache{next: next, clk: clk, entries: map[snapshotKey]*snapshotEntry{}}
}

func (c *snapshotCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != probev1connect.PublicServiceGetSnapshotProcedure {
		c.next.ServeHTTP(w, r)
		return
	}
	key, ok := canonicalSnapshotRequest(r)
	if !ok {
		c.next.ServeHTTP(w, r)
		return
	}
	status, header, body := c.lookup(key, r)
	// 存下的头在条目里共享，只读；写回时逐项复制到这个响应自己的头里。
	dst := w.Header()
	for k, v := range header {
		dst[k] = slices.Clone(v)
	}
	dst.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// lookup 返回窗口内的缓存，或者调 connect 处理器取一份新的。只有"状态 200 且 connect 实际用的压缩与键一致"才入缓存：
// 键里的压缩是 negotiatedCompression 对 connect 协商的复刻，复刻与实际不符时只损失命中率，不会把与键不符的字节
// 发给后来的请求。写回客户端在条目锁之外进行，慢客户端不挡同键的其他请求。
func (c *snapshotCache) lookup(key snapshotKey, r *http.Request) (int, http.Header, []byte) {
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &snapshotEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.filled && c.clk.Mono() < e.expires {
		return e.status, e.header, e.body
	}
	rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
	c.next.ServeHTTP(rec, r)
	body := rec.body.Bytes()
	if rec.status == http.StatusOK && rec.header.Get("Content-Encoding") == contentEncoding(key.compression) {
		e.filled, e.status, e.header, e.body, e.expires = true, rec.status, rec.header, body, c.clk.Mono()+snapshotTTL
	}
	return rec.status, rec.header, body
}

// canonicalSnapshotRequest 只认 connect-web 与 curl 常用的规范形态。其余形态（base64 包着的 JSON、带压缩的请求、
// 多余的空白或字段、别的协议版本、超时头……）交给 connect 自己解码与报错，不走缓存：缓存键不含请求内容，
// 缓存只能回答 connect 必然以同样方式成功处理的请求——宁可少命中，不可替 connect 接受它会拒绝的请求。
func canonicalSnapshotRequest(r *http.Request) (snapshotKey, bool) {
	if len(r.Header.Values("Connect-Timeout-Ms")) != 0 {
		return snapshotKey{}, false
	}
	key := snapshotKey{compression: negotiatedCompression(r.Header.Get("Accept-Encoding"))}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		for _, name := range []string{"connect", "encoding", "message", "base64", "compression"} {
			if len(q[name]) > 1 {
				return snapshotKey{}, false
			}
		}
		if v, ok := q["connect"]; ok && v[0] != "v1" {
			return snapshotKey{}, false
		}
		if v, ok := q["compression"]; ok && v[0] != "identity" {
			return snapshotKey{}, false
		}
		msg, hasMsg := q["message"]
		b64, hasB64 := q["base64"]
		switch q.Get("encoding") {
		case "json":
			if !hasMsg || msg[0] != "{}" || hasB64 {
				return snapshotKey{}, false
			}
		case "proto":
			if !hasMsg || msg[0] != "" || hasB64 && b64[0] != "1" {
				return snapshotKey{}, false
			}
		default:
			return snapshotKey{}, false
		}
		key.get, key.codec = true, q.Get("encoding")
		return key, true
	case http.MethodPost:
		ct := r.Header.Values("Content-Type")
		if len(ct) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
			return snapshotKey{}, false
		}
		if v := r.Header.Values("Connect-Protocol-Version"); len(v) > 1 || len(v) == 1 && v[0] != "1" {
			return snapshotKey{}, false
		}
		var want string
		switch ct[0] {
		case "application/json":
			key.codec, want = "json", "{}"
		case "application/proto":
			key.codec, want = "proto", ""
		default:
			return snapshotKey{}, false
		}
		// 多读一个字节就能判定正文是否恰为 want；读出的字节放回请求体，connect 照常读到完整正文。
		head, err := io.ReadAll(io.LimitReader(r.Body, int64(len(want)+1)))
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), r.Body), Closer: r.Body}
		if err != nil || string(head) != want {
			return snapshotKey{}, false
		}
		return key, true
	}
	return snapshotKey{}, false
}

type readCloser struct {
	io.Reader
	io.Closer
}

// negotiatedCompression 复刻 connect（v1.21.0）为不带压缩的 unary 请求选择响应压缩的规则：只看第一行
// Accept-Encoding（调用方传 Header.Get 的结果，与 connect 取这个头的方式相同），按逗号与空格切分，
// 取第一个服务端注册了的名字；本服务只有 connect 默认的 gzip。gzip 只在请求方列出了它时才会被选中，
// 所以按键命中的字节总是请求方能解开的。
func negotiatedCompression(accept string) string {
	for _, name := range strings.FieldsFunc(accept, func(r rune) bool { return r == ',' || r == ' ' }) {
		if name == "gzip" {
			return "gzip"
		}
	}
	return "identity"
}

// contentEncoding 是某种压缩在响应头里的写法：identity 不写 Content-Encoding。
func contentEncoding(compression string) string {
	if compression == "identity" {
		return ""
	}
	return compression
}

// responseRecorder 收下 connect 处理器的完整响应；unary 处理器返回时头与正文都已写完。
type responseRecorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
