// @author: kevinkelly
// @license: Apache Licence
// @file: sqlutil.go
// @time: 2026/10/28

package sr

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// 平台拼接的 SQL 只允许“标识符白名单 + 转义字面量”两种动态内容，避免注入。

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidIdent 判断是否为安全的库 / 表 / 资源组 / 作业名。
func ValidIdent(s string) bool { return identRe.MatchString(s) }

// Quote 返回加反引号的标识符；不合法时 ok 为 false。
func Quote(s string) (string, bool) {
	if !ValidIdent(s) {
		return "", false
	}
	return "`" + s + "`", true
}

// Lit 返回双引号字符串字面量，转义反斜杠、双引号和换行；含控制字符时报错。
func Lit(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n' || r == '\r' || r == 0:
			return "", errors.New("文本中不能包含换行或控制字符")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// MustLit 同 Lit，调用前已校验过内容时使用（出错返回空串字面量）。
func MustLit(s string) string {
	l, err := Lit(s)
	if err != nil {
		return `""`
	}
	return l
}

// Value 把参数值渲染为 SQL：布尔 / 数字原样，其余按字符串字面量。
func Value(v string) (string, error) {
	switch strings.ToLower(v) {
	case "true", "false":
		return strings.ToLower(v), nil
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil && !strings.ContainsAny(v, "eE+ ") && v != "" && !strings.HasPrefix(v, ".") && !strings.HasSuffix(v, ".") {
		return v, nil
	}
	return Lit(v)
}

// Exec 执行一条不返回结果集的语句（DDL / 管理命令）。
func (c *Client) Exec(ctx context.Context, q string) error {
	_, err := c.DB.ExecContext(ctx, q)
	return err
}

// MaskSecrets 把 SQL 文本中的密钥类属性值替换为 ***（用于审计与展示）。
var secretProp = regexp.MustCompile(`(?i)("[^"]*(?:secret|password|passwd|access_key|token)[^"]*"\s*=\s*)"(?:[^"\\]|\\.)*"`)

func MaskSecrets(q string) string { return secretProp.ReplaceAllString(q, `$1"***"`) }

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}( \d{2}:\d{2}:\d{2})?$`)
