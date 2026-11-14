// @author: kevinkelly
// @license: Apache Licence
// @file: main.go
// @time: 2026/10/28

// sr-ops 是 StarRocks 运维平台服务端：独立部署，托管前端并提供 /api。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"starrocks-ops/internal/api"
	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/inspect"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

var version = "1.7.5-dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8766", "监听地址")
	dataDir := flag.String("data", "data", "数据目录（元数据、报告、密钥、审计日志）")
	webDir := flag.String("web", "..", "前端目录（包含 index.html、css/、js/）")
	proxies := flag.String("trusted-proxies", "", "可信反向代理的 IP / CIDR（逗号分隔）；只有来自这些地址的请求才采信 X-Forwarded-For 作为来源 IP")
	flag.Parse()
	if err := api.SetTrustedProxies(*proxies); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(*webDir, "index.html")); err != nil {
		log.Fatalf("前端目录 %s 下没有 index.html（用 -web 指定）", *webDir)
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	box, err := secure.LoadOrCreateKey(filepath.Join(*dataDir, "secret.key"))
	if err != nil {
		log.Fatal(err)
	}
	keys, err := sshx.LoadOrCreate(filepath.Join(*dataDir, "ssh"))
	if err != nil {
		log.Fatal(err)
	}
	mgr := collect.NewManager(box)
	runner := &inspect.Runner{Store: st, Mgr: mgr, Keys: keys, Box: box}
	srv := &api.Server{Store: st, Mgr: mgr, Runner: runner, Keys: keys, Box: box, WebDir: *webDir, Version: version, ICP: loadICP(), Sessions: api.NewSessions()}
	if err := srv.Bootstrap(); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, c := range st.Clusters() {
		mgr.Sampler.Watch(c)
	}
	runner.StartScheduler(ctx)
	srv.StartMaintenance(ctx)
	watchExpire(ctx, loadExpire())

	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("StarRocks 运维平台 %s 已启动：http://%s/", version, *listen)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hs.Shutdown(shut)
}
