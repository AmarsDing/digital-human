package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type panelRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// 面板路由契约：panels.go 注册的共享路由必须在清单里，反之亦然。
// T1 新增的后端专用路由单独登记；本任务不修改并行前端 web/ 清单。
func TestPanelRouteCoverage(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/services/api/panel-routes.json")
	if err != nil {
		t.Fatalf("读取面板路由清单失败: %v", err)
	}
	var routes []panelRoute
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatalf("解析面板路由清单失败: %v", err)
	}
	// 80 + 5（F 批礼物/贵宾切片：patrons×5）= 85。
	if len(routes) != 85 {
		t.Fatalf("面板路由数量=%d want 85", len(routes))
	}

	src, err := os.ReadFile("panels.go")
	if err != nil {
		t.Fatalf("读取 panels.go 失败: %v", err)
	}
	text := string(src)
	for _, r := range routes {
		needle := "r." + r.Method + "(\"" + r.Path + "\""
		if !strings.Contains(text, needle) {
			t.Fatalf("panels.go 缺少路由: %s %s", r.Method, r.Path)
		}
	}
	backendOnly := []panelRoute{
		{Method: "POST", Path: "/api/v1/live-drafts/{id}/apply"},
		{Method: "GET", Path: "/api/v1/effects"},
		{Method: "POST", Path: "/api/v1/effects/{id}/reconcile"},
		{Method: "GET", Path: "/api/v1/song-requests"},
		{Method: "POST", Path: "/api/v1/song-requests/{id}/approve"},
		{Method: "POST", Path: "/api/v1/song-requests/{id}/reject"},
		{Method: "POST", Path: "/api/v1/song-requests/{id}/cancel"},
	}
	for _, r := range backendOnly {
		needle := "r." + r.Method + "(\"" + r.Path + "\""
		if !strings.Contains(text, needle) {
			t.Fatalf("panels.go 缺少后端专用路由: %s %s", r.Method, r.Path)
		}
	}
	// 反向：源码里的注册数不能多于共享清单与后端专用清单之和。
	if got := strings.Count(text, "r.GET(\"") + strings.Count(text, "r.POST(\"") +
		strings.Count(text, "r.DELETE(\"") + strings.Count(text, "r.PUT(\""); got != len(routes)+len(backendOnly) {
		t.Fatalf("panels.go 注册路由=%d，共享清单=%d，后端专用=%d", got, len(routes), len(backendOnly))
	}
}
