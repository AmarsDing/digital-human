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

// 面板路由契约：panels.go 注册的每条路由都必须在共享清单里，反之亦然（80/80）。
// 清单同时被前端 coverage 测试读取，避免两侧漏接。
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
	// 反向：源码里的注册数不能多于清单（防止新增路由忘记登记）。
	if got := strings.Count(text, "r.GET(\"") + strings.Count(text, "r.POST(\"") +
		strings.Count(text, "r.DELETE(\"") + strings.Count(text, "r.PUT(\""); got != len(routes) {
		t.Fatalf("panels.go 注册路由=%d，清单=%d", got, len(routes))
	}
}
