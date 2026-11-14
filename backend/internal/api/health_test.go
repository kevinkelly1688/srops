// @author: kevinkelly
// @license: Apache Licence
// @file: health_test.go
// @time: 2026/10/28

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// 健康检查（无需登录）带上配置文件里的 ICP 备案号，登录页据此展示。
func TestHealthICP(t *testing.T) {
	for _, icp := range []string{"京ICP备12345678号-1", ""} {
		s := &Server{Version: "test", ICP: icp}
		w := httptest.NewRecorder()
		s.health(w, httptest.NewRequest("GET", "/api/health", nil))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["icp"] != icp || got["version"] != "test" || got["mode"] != "live" {
			t.Fatalf("ICP=%q 时 health 返回不对：%v", icp, got)
		}
	}
}
