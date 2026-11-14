// @author: kevinkelly
// @license: Apache Licence
// @file: s3_test.go
// @time: 2026/10/28

package s3x

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// AWS 官方文档中公布的 SigV4 示例（GET Object 带 Range、GET Bucket 带 prefix），用来验证签名实现。
func TestSigV4AWSVectors(t *testing.T) {
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	const ak, sk = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := authorization("GET", "/test.txt", url.Values{}, map[string]string{"host": "examplebucket.s3.amazonaws.com", "range": "bytes=0-9", "x-amz-content-sha256": empty, "x-amz-date": "20130524T000000Z"}, empty, ak, sk, "us-east-1", "s3", now)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if strings.ReplaceAll(got, ", ", ",") != want {
		t.Fatalf("GET Object：\n got %s\nwant %s", got, want)
	}
	got = authorization("GET", "/", url.Values{"max-keys": {"2"}, "prefix": {"J"}}, map[string]string{"host": "examplebucket.s3.amazonaws.com", "x-amz-content-sha256": empty, "x-amz-date": "20130524T000000Z"}, empty, ak, sk, "us-east-1", "s3", now)
	want = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-content-sha256;x-amz-date,Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"
	if strings.ReplaceAll(got, ", ", ",") != want {
		t.Fatalf("GET Bucket：\n got %s\nwant %s", got, want)
	}
	if uriEncode("a b/c中~", false) != "a%20b/c%E4%B8%AD~" || uriEncode("a/b", true) != "a%2Fb" {
		t.Fatal("uriEncode")
	}
}

// mockS3 是内存中的 S3：路径风格，校验签名头与 Content-MD5。
type mockS3 struct {
	srv    *httptest.Server
	mu     sync.Mutex
	objs   map[string]string // bucket/key → 内容
	pageSz int
	fail   string // 非空：删除时返回该错误码
	calls  []string
}

func newMock(t *testing.T) *mockS3 {
	m := &mockS3{objs: map[string]string{}, pageSz: 1000}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockS3) put(bucket string, keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.objs[bucket+"/"+k] = "x"
	}
}

func (m *mockS3) keys(bucket, prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		if strings.HasPrefix(k, bucket+"/"+prefix) {
			out = append(out, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	sort.Strings(out)
	return out
}

func (m *mockS3) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") == "" {
		http.Error(w, `<Error><Code>AccessDenied</Code><Message>no signature</Message></Error>`, 403)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := parts[0]
	m.calls = append(m.calls, r.Method+" "+r.URL.RawQuery)
	switch {
	case r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range m.objs {
			if strings.HasPrefix(k, bucket+"/"+prefix) {
				keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
			}
		}
		sort.Strings(keys)
		start := 0
		if tok := r.URL.Query().Get("continuation-token"); tok != "" {
			fmt.Sscanf(tok, "t%d", &start)
		}
		end := start + m.pageSz
		trunc := end < len(keys)
		if end > len(keys) {
			end = len(keys)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf(`<ListBucketResult><IsTruncated>%v</IsTruncated>`, trunc))
		if trunc {
			b.WriteString(fmt.Sprintf(`<NextContinuationToken>t%d</NextContinuationToken>`, end))
		}
		for _, k := range keys[start:end] {
			var esc strings.Builder
			xml.EscapeText(&esc, []byte(k))
			b.WriteString(`<Contents><Key>` + esc.String() + `</Key><Size>1</Size></Contents>`)
		}
		b.WriteString(`</ListBucketResult>`)
		io.WriteString(w, b.String())
	case r.Method == "POST" && r.URL.Query().Has("delete"):
		body, _ := io.ReadAll(r.Body)
		sum := md5.Sum(body)
		if r.Header.Get("Content-Md5") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, `<Error><Code>BadDigest</Code><Message>md5</Message></Error>`, 400)
			return
		}
		var d struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &d); err != nil {
			http.Error(w, `<Error><Code>MalformedXML</Code></Error>`, 400)
			return
		}
		var errs strings.Builder
		for _, o := range d.Objects {
			if m.fail != "" {
				errs.WriteString(`<Error><Key>` + o.Key + `</Key><Code>` + m.fail + `</Code><Message>denied</Message></Error>`)
				continue
			}
			delete(m.objs, bucket+"/"+o.Key)
		}
		io.WriteString(w, `<DeleteResult>`+errs.String()+`</DeleteResult>`)
	default:
		http.Error(w, `<Error><Code>NotImplemented</Code></Error>`, 501)
	}
}

func client(m *mockS3) *Client {
	return &Client{Endpoint: m.srv.URL, AccessKey: "AK", SecretKey: "SK", PathStyle: true}
}

func TestListAndDeletePrefix(t *testing.T) {
	m := newMock(t)
	m.pageSz = 3
	for i := 0; i < 8; i++ {
		m.put("bk", fmt.Sprintf("repo/__ss_a/f%02d", i), fmt.Sprintf("repo/__ss_a/中 文/g%02d", i))
	}
	m.put("bk", "repo/__ss_ab/keep", "repo/__ss_b/keep", "other/x")
	c := client(m)

	objs, trunc, err := c.List(t.Context(), "bk", "repo/__ss_a/", 0)
	if err != nil || trunc || len(objs) != 16 {
		t.Fatalf("分页列出：%d %v %v", len(objs), trunc, err)
	}
	objs, trunc, _ = c.List(t.Context(), "bk", "repo/__ss_a/", 5)
	if !trunc || len(objs) != 5 {
		t.Fatalf("limit：%d %v", len(objs), trunc)
	}

	var progress []int
	n, err := c.DeletePrefix(t.Context(), "bk", "repo/__ss_a/", func(d int) { progress = append(progress, d) })
	if err != nil || n != 16 {
		t.Fatalf("删除：%d %v", n, err)
	}
	if left := m.keys("bk", "repo/__ss_a/"); len(left) != 0 {
		t.Fatalf("应删光：%v", left)
	}
	// 前缀以 / 结尾：__ss_ab、__ss_b 与其他目录不受影响
	if got := m.keys("bk", ""); strings.Join(got, ",") != "other/x,repo/__ss_ab/keep,repo/__ss_b/keep" {
		t.Fatalf("不应误删相邻目录：%v", got)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 16 {
		t.Fatalf("进度回调：%v", progress)
	}
	// 前缀保护
	for _, bad := range []string{"", "repo/__ss_a", "a//b/"} {
		if _, err := c.DeletePrefix(t.Context(), "bk", bad, nil); err == nil {
			t.Errorf("前缀 %q 应被拒绝", bad)
		}
	}
	// 空目录：0 个对象
	if n, err := c.DeletePrefix(t.Context(), "bk", "none/", nil); err != nil || n != 0 {
		t.Fatalf("空目录：%d %v", n, err)
	}
}

func TestDeleteErrorsAndEndpoints(t *testing.T) {
	m := newMock(t)
	m.put("bk", "p/a/1", "p/a/2")
	m.fail = "AccessDenied"
	c := client(m)
	if _, err := c.DeletePrefix(t.Context(), "bk", "p/a/", nil); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("单个对象删除失败应报错：%v", err)
	}
	if len(m.keys("bk", "p/a/")) != 2 {
		t.Fatal("失败时对象应保留")
	}
	// 未签名的请求会被拒绝；错误信息带上存储端的错误码
	bad := &Client{Endpoint: m.srv.URL, PathStyle: true}
	bad.AccessKey = ""
	if _, _, err := (&Client{Endpoint: "http://127.0.0.1:1", PathStyle: true, AccessKey: "AK", SecretKey: "SK"}).List(t.Context(), "bk", "x/", 0); err == nil || !strings.Contains(err.Error(), "连接对象存储失败") {
		t.Fatalf("连接失败：%v", err)
	}
	// 虚拟主机风格与端点省略协议
	u, err := (&Client{Endpoint: "oss-cn-hangzhou.aliyuncs.com"}).target("my-bucket", "a/b c")
	if err != nil || u.Host != "my-bucket.oss-cn-hangzhou.aliyuncs.com" || u.Scheme != "https" || u.Path != "/a/b c" {
		t.Fatalf("virtual-host：%v %v", u, err)
	}
	u, _ = (&Client{Endpoint: "http://minio:9000/", PathStyle: true}).target("bk", "a/b")
	if u.Host != "minio:9000" || u.Path != "/bk/a/b" {
		t.Fatalf("path-style：%v", u)
	}
	if _, err := (&Client{Endpoint: "http://"}).target("b", "k"); err == nil {
		t.Fatal("非法端点应报错")
	}
}
