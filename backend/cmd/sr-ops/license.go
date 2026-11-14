// @author: kevinkelly
// @license: Apache Licence
// @file: license.go
// @time: 2026/10/28

package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"
)

// 使用期限与 ICP 备案号来自配置文件 sr-ops.conf（systemd 以环境变量传入）：
//
//	SR_EXPIRE=2028-01-01   使用期限，含当天；未配置时用 defaultExpire
//	SR_ICP=...             页面底部展示的 ICP 备案号；空 = 不展示
//
// 到期后平台照常运行，页面不做任何提醒，只在日志里记录。
const (
	defaultExpire = "2028-01-01"
	dateLayout    = "2006-01-02"
)

// loadExpire 返回使用期限当天的 0 点；SR_EXPIRE 格式不对时按默认值处理并记一条日志。
func loadExpire() time.Time {
	s := strings.TrimSpace(os.Getenv("SR_EXPIRE"))
	if s == "" {
		s = defaultExpire
	}
	d, err := time.ParseInLocation(dateLayout, s, time.Local)
	if err != nil {
		log.Printf("配置项 SR_EXPIRE=%q 不是 YYYY-MM-DD 格式，按默认使用期限 %s 处理", s, defaultExpire)
		d, _ = time.ParseInLocation(dateLayout, defaultExpire, time.Local)
	}
	return d
}

// loadICP 返回要在页面底部展示的 ICP 备案号。
func loadICP() string { return strings.TrimSpace(os.Getenv("SR_ICP")) }

// expired 判断是否已过使用期限：期限当天结束后才算到期。
func expired(d, now time.Time) bool { return !now.Before(d.AddDate(0, 0, 1)) }

// checkExpire 到期则写一条日志。
func checkExpire(d, now time.Time) {
	if expired(d, now) {
		log.Printf("平台使用期限已于 %s 到期", d.Format(dateLayout))
	}
}

// watchExpire 启动时记录使用期限，之后每天检查一次（长期运行的服务跨过期限时也会留下日志）。
func watchExpire(ctx context.Context, d time.Time) {
	if !expired(d, time.Now()) {
		log.Printf("使用期限至 %s", d.Format(dateLayout))
	}
	checkExpire(d, time.Now())
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				checkExpire(d, time.Now())
			}
		}
	}()
}
