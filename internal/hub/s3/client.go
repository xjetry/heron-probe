// Package s3 实现备份所需的 S3 SigV4 操作，不依赖云厂商 SDK。
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xjetry/probe/internal/hub/outbound"
)

const responseLimit = 64 << 10

type Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	Secret    string
	// VirtualHost 为 false 时用 path-style，适用于 R2 与自定义 endpoint。
	VirtualHost bool
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// ValidateTarget 不回显配置值，避免 URL 中误填的凭据进入错误文本。
func ValidateTarget(endpoint, bucket string) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	return ValidateBucket(bucket)
}

// ValidateEndpoint 同样不回显配置值。主机只收签名与线上一致的写法：签名覆盖 URL 里的 host，而 net/http 在线上
// 把非 ASCII 主机转成 punycode、把 IPv6 zone 从 Host 头里去掉（Go 1.27.1 实测），两者不一致时每个请求都会被
// 服务端以签名不符拒绝。
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("backup.endpoint must be an http(s) URL without userinfo, query or fragment")
	}
	if strings.ContainsFunc(u.Host, func(r rune) bool { return r >= utf8.RuneSelf || r == '%' }) {
		return errors.New("backup.endpoint host must be ASCII without an IPv6 zone; write internationalized names in punycode (xn--...)")
	}
	return nil
}

func ValidateBucket(bucket string) error {
	if !bucketName.MatchString(bucket) || strings.Contains(bucket, "..") {
		return errors.New("backup.bucket must be a non-empty S3 bucket name (3-63 lowercase letters, digits, dots or hyphens)")
	}
	return nil
}

func ValidateSigningIdentifier(field, value string) error {
	if strings.Contains(value, "/") || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("backup.%s must not contain slash, whitespace or control characters", field)
	}
	return nil
}

func (c Config) Enabled() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.Secret != ""
}

type Client struct {
	cfg      Config
	endpoint *url.URL
	http     *http.Client
	now      func() time.Time
}

func New(cfg Config) (*Client, error) {
	if !cfg.Enabled() {
		return nil, errors.New("backup endpoint, bucket, access key and secret are required")
	}
	if err := ValidateTarget(cfg.Endpoint, cfg.Bucket); err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		return nil, errors.New("backup.region must not be empty")
	}
	for _, f := range []struct{ name, value string }{{"region", cfg.Region}, {"access_key", cfg.AccessKey}} {
		if err := ValidateSigningIdentifier(f.name, f.value); err != nil {
			return nil, err
		}
	}
	u, _ := url.Parse(cfg.Endpoint)
	client := outbound.NewClient()
	// 对象可能为 GB 级；通知的十秒总时限不适合完整传输。仍保留共享客户端的重定向禁令。
	client.Timeout = 30 * time.Minute
	return &Client{cfg: cfg, endpoint: u, http: client, now: time.Now}, nil
}

type Error struct {
	Kind       string
	StatusCode int
	// HTTP 失败时 Detail 保留响应前 200 字节，其他失败使用固定描述。
	// 由接收方决定的内容可能含敏感信息，不应公开下发。
	Detail string
	err    error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("s3 %s HTTP %d: %s", e.Kind, e.StatusCode, e.Detail)
	}
	return "s3 " + e.Kind + ": " + e.Detail
}
func (e *Error) Unwrap() error { return e.err }
func (e *Error) Retryable() bool {
	return e.Kind == "transport" || e.Kind == "http_status" && (e.StatusCode >= 500 || e.StatusCode == 408 || e.StatusCode == 429)
}

func fail(kind, detail string, err error) error { return &Error{Kind: kind, Detail: detail, err: err} }

func (c *Client) request(ctx context.Context, method, key string, query url.Values, body io.Reader, size int64, hash string) (*http.Response, error) {
	u := *c.endpoint
	base := strings.TrimRight(u.Path, "/")
	if c.cfg.VirtualHost {
		u.Host = c.cfg.Bucket + "." + u.Host
	} else {
		base += "/" + c.cfg.Bucket
	}
	u.Path = base + "/" + key
	u.RawPath = escape(u.Path, true)
	u.RawQuery = canonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fail("request", "cannot construct request", err)
	}
	req.ContentLength = size
	req.Header.Set("X-Amz-Date", c.now().UTC().Format("20060102T150405Z"))
	req.Header.Set("X-Amz-Content-Sha256", hash)
	canonical, signed := canonicalRequest(req, hash)
	_, auth := authorization(req, canonical, signed, c.cfg.Region, "s3", c.cfg.AccessKey, c.cfg.Secret)
	req.Header.Set("Authorization", auth)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fail("transport", "request failed", err)
	}
	if resp.StatusCode < 100 || resp.StatusCode > 999 {
		resp.Body.Close()
		return nil, fail("transport", "malformed HTTP status", nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, &Error{Kind: "http_status", StatusCode: resp.StatusCode, Detail: string(data)}
	}
	return resp, nil
}

// PutObject 从当前位置开始上传；读取器必须在散列与上传期间保持内容不变。
// 先散列再回到原位，不把整个数据库读入内存，也不使用 UNSIGNED-PAYLOAD。
func (c *Client) PutObject(ctx context.Context, key string, body io.ReadSeeker) error {
	if body == nil {
		return fail("request", "object body is required", nil)
	}
	start, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return fail("request", "cannot seek object", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx, body})
	if err != nil {
		return fail("request", "cannot hash object", err)
	}
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return fail("request", "cannot rewind object", err)
	}
	resp, err := c.request(ctx, http.MethodPut, key, nil, body, n, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseLimit))
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	resp, err := c.request(ctx, http.MethodDelete, key, nil, nil, 0, hashHex(nil))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseLimit))
	return nil
}

// GetObject 的上限由消费方按产物类型指定；超过上限、读中断或写失败均返回错误，
// 调用方必须丢弃失败时的部分产物，不得把截断文件当作成功备份。
func (c *Client) GetObject(ctx context.Context, key string, dst io.Writer, maxBytes int64) error {
	if dst == nil || maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return fail("request", "destination and positive bounded maxBytes are required", nil)
	}
	resp, err := c.request(ctx, http.MethodGet, key, nil, nil, 0, hashHex(nil))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return fail("response", "object exceeds maxBytes", nil)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return fail("response", "cannot copy object", err)
	}
	if n == maxBytes {
		var extra [1]byte
		if _, err := io.ReadFull(resp.Body, extra[:]); err != io.EOF {
			return fail("response", "object exceeds maxBytes or cannot finish reading", err)
		}
	}
	return nil
}

type Object struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

func (c *Client) ListObjectsV2(ctx context.Context, prefix string) ([]Object, error) {
	var objects []Object
	seen := map[string]bool{}
	token := ""
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"10"}, "encoding-type": {"url"}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		resp, err := c.request(ctx, http.MethodGet, "", query, nil, 0, hashHex(nil))
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
		resp.Body.Close()
		if err != nil {
			return nil, fail("response", "cannot read list response", err)
		}
		if len(data) > responseLimit {
			return nil, fail("response", "list response exceeds 65536 bytes", nil)
		}
		var page struct {
			XMLName   xml.Name `xml:"ListBucketResult"`
			Truncated bool     `xml:"IsTruncated"`
			Next      string   `xml:"NextContinuationToken"`
			Encoding  string   `xml:"EncodingType"`
			Objects   []Object `xml:"Contents"`
		}
		if err := xml.Unmarshal(data, &page); err != nil {
			return nil, fail("response", "invalid list XML", err)
		}
		for _, obj := range page.Objects {
			if page.Encoding == "url" {
				obj.Key, err = url.PathUnescape(obj.Key)
				if err != nil {
					return nil, fail("response", "invalid encoded object key", err)
				}
			}
			objects = append(objects, obj)
		}
		if !page.Truncated {
			return objects, nil
		}
		if page.Next == "" || seen[page.Next] {
			return nil, fail("response", "missing or repeated continuation token", nil)
		}
		seen[page.Next] = true
		token = page.Next
	}
}
