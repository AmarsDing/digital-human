package server

import (
	"encoding/json"
	"io"
	"net/http"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/live"
)

// registerPreflightRoute 深化方案 v2 §7.5：唯一预检入口 POST /api/v1/live/preflight，
// 返回 check_id/preflight_token/ready/mode/checks/observed_at/expires_at（30s 新鲜期）。
//
// 独立注册（不进 registerPanelRoutes）：面板路由清单 web/src/services/api/panel-routes.json
// 属前端工作区，本切片不改 web/；该路由与面板面板路由同走鉴权 filter（srv.Route("/")）。
func registerPreflightRoute(srv *khttp.Server, pf *live.Preflight) {
	if pf == nil {
		return // fail-closed：未装配时无路由，public Start 复核必失败
	}
	r := srv.Route("/")
	r.POST("/api/v1/live/preflight", func(c khttp.Context) error {
		var req live.PreflightRequest
		// 允许空 body（默认 mode=public-live）；解析失败按 400。
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil && err != io.EOF {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		}
		return c.JSON(http.StatusOK, pf.Run(c.Request().Context(), req))
	})
}
