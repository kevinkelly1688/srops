// @author: kevinkelly
// @license: Apache Licence
// @file: secure_test.go
// @time: 2026/10/28

package secure

import (
	"path/filepath"
	"testing"
)

func TestBoxRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	b, err := LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b.Encrypt("s3cr3t")
	if enc == "s3cr3t" {
		t.Fatal("未加密")
	}
	b2, _ := LoadOrCreateKey(p)
	if got, err := b2.Decrypt(enc); err != nil || got != "s3cr3t" {
		t.Fatalf("解密失败：%q %v", got, err)
	}
}

func TestPassword(t *testing.T) {
	h, _ := HashPassword("Admin@2026xyz")
	if !CheckPassword("Admin@2026xyz", h) || CheckPassword("admin@2026xyz", h) {
		t.Fatal("密码校验错误")
	}
	if ValidatePassword("short1A!", 10) == nil || ValidatePassword("NoDigitsHere!", 10) == nil || ValidatePassword("Admin@2026xyz", 10) != nil {
		t.Fatal("复杂度校验错误")
	}
	if ValidatePassword(RandomPassword(), 10) != nil {
		t.Fatal("随机初始密码不满足复杂度")
	}
}
