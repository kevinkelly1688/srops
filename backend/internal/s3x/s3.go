// @author: kevinkelly
// @license: Apache Licence
// @file: s3.go
// @time: 2026/10/28

// Package s3x 是一个最小的 S3 兼容存储客户端（AWS Signature V4），只提供“按前缀列出 / 删除对象”，
// 用于清理 StarRocks 备份仓库中过期的快照目录。兼容 AWS S3、MinIO、阿里云 OSS、腾讯云 COS 等 S3 协议端点。
package s3x

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client 访问一个 S3 兼容端点。
type Client struct {
	Endpoint  string // 如 https://s3.cn-north-1.amazonaws.com.cn 或 http://minio:9000；可省略协议（默认 https）
	Region    string // 默认 us-east-1
	AccessKey string
	SecretKey string
	PathStyle bool // true：endpoint/bucket/key；false：bucket.endpoint/key
	HTTP      *http.Client
	Now       func() time.Time
}

// Object 是列表中的一个对象。
type Object struct {
	Key  string
	Size int64
}

func (c *Client) region() string {
	if c.Region == "" {
		return "us-east-1"
	}
	return c.Region
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// target 返回请求的 URL（不含查询串）与 Host。
func (c *Client) target(bucket, key string) (u *url.URL, err error) {
	ep := strings.TrimSpace(c.Endpoint)
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	if u, err = url.Parse(ep); err != nil || u.Host == "" {
		return nil, errors.New("存储端点格式不正确")
	}
	base := strings.TrimRight(u.Path, "/")
	if c.PathStyle {
		u.Path = base + "/" + bucket + "/" + key
	} else {
		u.Host = bucket + "." + u.Host
		u.Path = base + "/" + key
	}
	u.RawPath = ""
	return u, nil
}

// ---------- Signature V4 ----------

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// uriEncode 按 SigV4 规则编码；encodeSlash 为 false 时保留 /。
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// authorization 计算 Authorization 头；headers 的名称必须是小写，并且都参与签名（必须包含 host、x-amz-date）。
func authorization(method, path string, query url.Values, headers map[string]string, payloadHash, ak, sk, region, service string, now time.Time) string {
	var qs []string
	for k, vs := range query {
		for _, v := range vs {
			qs = append(qs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(qs)
	var names []string
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := method + "\n" + uriEncode(path, false) + "\n" + strings.Join(qs, "&") + "\n" + ch.String() + "\n" + signed + "\n" + payloadHash
	date := now.UTC().Format("20060102")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + now.UTC().Format("20060102T150405Z") + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256(hmacSHA256(hmacSHA256(hmacSHA256([]byte("AWS4"+sk), date), region), service), "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	return "AWS4-HMAC-SHA256 Credential=" + ak + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
}

func (c *Client) do(ctx context.Context, method, bucket, key string, query url.Values, body []byte, extra map[string]string) ([]byte, error) {
	u, err := c.target(bucket, key)
	if err != nil {
		return nil, err
	}
	now := c.now()
	hash := sha256Hex(body)
	headers := map[string]string{"host": u.Host, "x-amz-content-sha256": hash, "x-amz-date": now.UTC().Format("20060102T150405Z")}
	for k, v := range extra {
		headers[strings.ToLower(k)] = v
	}
	auth := authorization(method, u.Path, query, headers, hash, c.AccessKey, c.SecretKey, c.region(), "s3", now)
	u.RawQuery = encodeQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if k != "host" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("User-Agent", "sr-ops")
	resp, err := c.httpc().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s %s：%v", ue.Op, u.Host, ue.Err)
		}
		return nil, fmt.Errorf("连接对象存储失败：%v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Code, Message string }
		xml.Unmarshal(rb, &e)
		if e.Code != "" {
			return nil, fmt.Errorf("对象存储返回 %s：%s（HTTP %d）", e.Code, e.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("对象存储返回 HTTP %d：%s", resp.StatusCode, shorten(rb))
	}
	return rb, nil
}

func encodeQuery(q url.Values) string {
	var qs []string
	for k, vs := range q {
		for _, v := range vs {
			qs = append(qs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(qs)
	return strings.Join(qs, "&")
}

func shorten(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// List 列出 prefix 下的对象（最多 limit 个；limit <= 0 表示不限）。truncated 表示还有更多。
func (c *Client) List(ctx context.Context, bucket, prefix string, limit int) (objs []Object, truncated bool, err error) {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		rb, err := c.do(ctx, http.MethodGet, bucket, "", q, nil, nil)
		if err != nil {
			return nil, false, err
		}
		var r struct {
			IsTruncated bool   `xml:"IsTruncated"`
			Next        string `xml:"NextContinuationToken"`
			Contents    []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
		}
		if err := xml.Unmarshal(rb, &r); err != nil {
			return nil, false, fmt.Errorf("对象存储返回无法解析：%s", shorten(rb))
		}
		for _, o := range r.Contents {
			if limit > 0 && len(objs) >= limit {
				return objs, true, nil
			}
			objs = append(objs, Object{Key: o.Key, Size: o.Size})
		}
		if !r.IsTruncated || r.Next == "" {
			return objs, false, nil
		}
		token = r.Next
	}
}

// DeletePrefix 删除 prefix 下的全部对象，返回删除的数量。prefix 必须以 / 结尾，避免误删同名前缀的其他目录。
// progress 在每批删除后回调（累计删除数）。
func (c *Client) DeletePrefix(ctx context.Context, bucket, prefix string, progress func(deleted int)) (int, error) {
	if prefix == "" || !strings.HasSuffix(prefix, "/") || strings.Contains(prefix, "//") {
		return 0, errors.New("删除前缀必须是以 / 结尾的目录")
	}
	deleted := 0
	for {
		objs, _, err := c.List(ctx, bucket, prefix, 1000)
		if err != nil {
			return deleted, err
		}
		if len(objs) == 0 {
			return deleted, nil
		}
		var body bytes.Buffer
		body.WriteString("<Delete><Quiet>true</Quiet>")
		for _, o := range objs {
			if !strings.HasPrefix(o.Key, prefix) {
				return deleted, fmt.Errorf("列表返回了前缀之外的对象 %q，已停止", o.Key)
			}
			body.WriteString("<Object><Key>")
			xml.EscapeText(&body, []byte(o.Key))
			body.WriteString("</Key></Object>")
		}
		body.WriteString("</Delete>")
		sum := md5.Sum(body.Bytes())
		rb, err := c.do(ctx, http.MethodPost, bucket, "", url.Values{"delete": {""}}, body.Bytes(),
			map[string]string{"content-md5": base64.StdEncoding.EncodeToString(sum[:]), "content-type": "application/xml"})
		if err != nil {
			return deleted, err
		}
		var r struct {
			Errors []struct{ Key, Code, Message string } `xml:"Error"`
		}
		xml.Unmarshal(rb, &r)
		if len(r.Errors) > 0 {
			return deleted, fmt.Errorf("删除 %s 失败：%s %s（共 %d 个对象失败）", r.Errors[0].Key, r.Errors[0].Code, r.Errors[0].Message, len(r.Errors))
		}
		deleted += len(objs)
		if progress != nil {
			progress(deleted)
		}
		if len(objs) < 1000 {
			// 不足一页，再列一次确认已经清空
			rest, _, err := c.List(ctx, bucket, prefix, 1)
			if err != nil {
				return deleted, err
			}
			if len(rest) == 0 {
				return deleted, nil
			}
		}
		if ctx.Err() != nil {
			return deleted, ctx.Err()
		}
	}
}
