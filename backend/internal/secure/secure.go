// @author: kevinkelly
// @license: Apache Licence
// @file: secure.go
// @time: 2026/10/28

// Package secure：集群密码 / SSH 密码的 AES-256-GCM 加密，以及平台用户密码的 PBKDF2 哈希。
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Box 用一个 32 字节密钥加解密。
type Box struct{ aead cipher.AEAD }

// LoadOrCreateKey 读取密钥文件，不存在则生成（权限 0600）。
func LoadOrCreateKey(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("密钥文件 %s 长度应为 32 字节", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (b *Box) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("密文格式错误")
	}
	n := b.aead.NonceSize()
	out, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("解密失败（密钥文件是否被替换？）")
	}
	return string(out), nil
}

const iter = 210000

// HashPassword 返回 pbkdf2-sha256$迭代次数$salt$hash。
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h, err := pbkdf2.Key(sha256.New, pw, salt, iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

// CheckPassword 以常量时间比较。
func CheckPassword(pw, stored string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	n, err := strconv.Atoi(p[1])
	if err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[2])
	want, err2 := base64.RawStdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, n, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword 校验密码复杂度：至少 minLen 位，含大写、小写、数字、符号。
func ValidatePassword(pw string, minLen int) error {
	var up, lo, dg, sy bool
	for _, r := range pw {
		switch {
		case r >= 'A' && r <= 'Z':
			up = true
		case r >= 'a' && r <= 'z':
			lo = true
		case r >= '0' && r <= '9':
			dg = true
		default:
			sy = true
		}
	}
	if len([]rune(pw)) < minLen || !up || !lo || !dg || !sy {
		return fmt.Errorf("密码至少 %d 位，且须包含大写、小写字母、数字和符号", minLen)
	}
	return nil
}

// RandomPassword 生成满足复杂度要求的随机密码（用于初始管理员）。
func RandomPassword() string { return RandomPasswordN(14) }

// RandomPasswordN 生成 n+4 位的随机密码（含大小写、数字、符号），n 不足 14 时按 14。
func RandomPasswordN(n int) string {
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if n < 14 {
		n = 14
	}
	b := make([]byte, n)
	rand.Read(b)
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = set[int(v)%len(set)]
	}
	return "Sr" + string(out) + "#7"
}

// Token 返回 32 字节随机令牌（会话 ID）。
func Token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
