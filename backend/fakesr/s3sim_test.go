// @author: kevinkelly
// @license: Apache Licence
// @file: s3sim_test.go
// @time: 2026/10/28

package fakesr

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestS3SimLinksSnapshots(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueryPort, cfg.SSHPort = 0, -1
	cfg.FEHTTPBase, cfg.BEHTTPBase, cfg.CNHTTPBase = 0, 0, 0
	cfg.S3, cfg.S3Port = true, 0
	c, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if c.S3Addr() == "" {
		t.Fatal("S3 应已启动")
	}
	// 初始快照在 S3 里有文件
	if keys := c.S3Keys("sr-backup", "prod-sr-01/__starrocks_repository_repo_s3_prod/__ss_snap_dws_20261005/"); len(keys) != 4 {
		t.Fatalf("初始快照文件：%v", keys)
	}
	if got := strings.Join(c.SnapshotNames("repo_s3_prod"), ","); got != "snap_ads_20261006,snap_dws_20261005" {
		t.Fatalf("快照：%s", got)
	}
	// 未签名的请求被拒绝
	resp, err := http.Get("http://" + c.S3Addr() + "/sr-backup?list-type=2")
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("未签名应 403：%v %v", resp, err)
	}
	resp.Body.Close()
	// 直接删除对象后，快照消失
	c.s3.mu.Lock()
	for k := range c.s3.objs {
		if strings.Contains(k, "__ss_snap_dws_20261005/") {
			delete(c.s3.objs, k)
		}
	}
	c.s3.mu.Unlock()
	if got := strings.Join(c.SnapshotNames("repo_s3_prod"), ","); got != "snap_ads_20261006" {
		t.Fatalf("对象删除后快照应消失：%s", got)
	}
	// 新增快照同时生成文件
	if err := c.AddSnapshot("repo_s3_prod", "auto_x_1", time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(c.S3Keys("sr-backup", "prod-sr-01/__starrocks_repository_repo_s3_prod/__ss_auto_x_1/")) != 4 {
		t.Fatal("AddSnapshot 应生成文件")
	}
	// HDFS 仓库不受影响
	if got := strings.Join(c.SnapshotNames("repo_hdfs_ro"), ","); got != "snap_dim_20261001" {
		t.Fatalf("hdfs：%s", got)
	}
}
