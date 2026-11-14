// @author: kevinkelly
// @license: Apache Licence
// @file: license_test.go
// @time: 2026/10/28

package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.ParseInLocation(dateLayout, s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestLoadExpire(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	t.Setenv("SR_EXPIRE", "")
	if got := loadExpire(); !got.Equal(day("2028-01-01")) {
		t.Fatalf("未配置应使用默认 2028-01-01：%v", got)
	}
	t.Setenv("SR_EXPIRE", " 2030-05-06 ")
	if got := loadExpire(); !got.Equal(day("2030-05-06")) {
		t.Fatalf("应读取配置的日期：%v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("合法配置不应有日志：%s", buf.String())
	}
	t.Setenv("SR_EXPIRE", "2030/05/06")
	if got := loadExpire(); !got.Equal(day("2028-01-01")) {
		t.Fatalf("格式不对应回到默认值：%v", got)
	}
	if !strings.Contains(buf.String(), "SR_EXPIRE") {
		t.Fatalf("格式不对应记一条日志：%q", buf.String())
	}
}

func TestExpireOnlyLogs(t *testing.T) {
	d := day("2028-01-01")
	if expired(d, day("2028-01-01").Add(23*time.Hour+59*time.Minute)) {
		t.Fatal("期限当天仍在有效期内")
	}
	if !expired(d, day("2028-01-02")) {
		t.Fatal("期限当天结束后应视为到期")
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	checkExpire(d, day("2027-12-31"))
	if buf.Len() != 0 {
		t.Fatalf("未到期不应有日志：%q", buf.String())
	}
	checkExpire(d, day("2028-01-02"))
	if !strings.Contains(buf.String(), "2028-01-01 到期") {
		t.Fatalf("到期后应打印日志：%q", buf.String())
	}
}

func TestLoadICP(t *testing.T) {
	t.Setenv("SR_ICP", "")
	if loadICP() != "" {
		t.Fatal("未配置应为空")
	}
	t.Setenv("SR_ICP", "  京ICP备12345678号-1 ")
	if got := loadICP(); got != "京ICP备12345678号-1" {
		t.Fatalf("应去掉首尾空白：%q", got)
	}
}
