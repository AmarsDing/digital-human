package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/patron"
	"github.com/teamwork/digital-human/internal/biz/persona"
	"github.com/teamwork/digital-human/internal/biz/recording"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/biz/tts"
	"github.com/teamwork/digital-human/internal/biz/vidukeys"
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/llm"
	"github.com/teamwork/digital-human/pkg/logx"
)

// maxUploadFileBytes 单文件上传上限。
const maxUploadFileBytes = 50 << 20 // 50MB

// PanelDeps 是 M3 面板后端的依赖集合；nil 服务会被跳过注册，便于渐进接线。
type PanelDeps struct {
	TTS          *tts.Service
	Actions      *action.Service
	Choreo       *choreo.Service
	Koubo        *koubo.Service
	Refs         *refs.Service
	Songs        *song.Service
	SongRequests song.RequestStore
	Archive      *archive.Service
	Billing      *billing.Service
	Danmaku      *danmaku.Service
	Personas     *persona.Service
	// Patrons F29/G24 贵宾名单与欢迎（蓝本 bridge.py:878-940 五路由）；nil 跳过注册。
	Patrons *patron.Service
	// Avatars 数字人档案服务（G 批 M2，v2 §3.1 11 RPC + 图片路由）；nil 跳过注册。
	Avatars *avatar.Service
	// AvatarImages 图片上传/读取管线（§5.3）；nil 时上传与图片路由跳过注册。
	AvatarImages *avatar.ImageService
	// AvatarSync sync-vidu 异步管线（G 批 M3，v3 §2.2 202 任务化）；
	// nil 时 sync-vidu 返回 503 vidu_key_unconfigured（fail-closed）。
	AvatarSync *avatar.SyncService
	// ResetLLM 人设切换后立即清当前场次 LLM 记忆（G04），由 live.Manager.ResetLlm 装配；nil 跳过。
	ResetLLM func(ctx context.Context) int
	// Chat N1 管理员对话调试（蓝本 bridge.py:469 /api/chat）：发文本走 chat.Manager
	// （独立 admin 记忆 + 人设），应答经 sink 上屏播报；返回 reply 文本。
	// 未开播 → 409 not_live（对齐蓝本 engine.chat "没有进行中的会话"）；nil 跳过注册。
	Chat func(ctx context.Context, text string) (string, error)
	// ViduKeys F42/G18 多 key 管理（蓝本 bridge.py:1458,1476 /api/vidu/keys）；nil 跳过注册。
	ViduKeys *vidukeys.Service
	// ViduKeyCurrent 返回当前生效 key 明文（蓝本首访落库语义：列表为空时把当前 key 落为 active）；
	// 仅服务层内部使用，绝不写进响应。nil 时 Adopt 跳过。
	ViduKeyCurrent func() string
	// Settings F34 录播开关持久化（misc.record_enabled）；nil 时录播 GET/POST 跳过注册。
	Settings *settings.Service
	// Recording F34/N2 云录制 Supervisor（蓝本 bridge.py:408-434 /api/record）；
	// nil（dev 模式或未开通）时 fetch 返回 503 recording_unavailable（fail-closed），
	// GET 仍返回开关 + unavailable 状态。
	Recording *recording.Supervisor
	// Credits F08/F36/G03/N3 实时额度查询（蓝本 bridge.py:1508 /api/credits 透传 vidu credits）；
	// nil（无凭据/dev）时返回 200 明确空态 configured=false，不报错。
	Credits func(ctx context.Context) (map[string]any, error)
	// Contentpkg 内容包草稿原子应用；nil 时不注册发布路由。
	Contentpkg *contentpkg.Service
	// Effects 与 InteractionDispatcher 提供 unknown 副作用查询和人工对账。
	Effects               interaction.Store
	InteractionDispatcher *interaction.Dispatcher
	// StorageRoot 是 import-folder 允许的白名单根目录；为空时回退到 TTS 素材库所在根。
	StorageRoot string
}

// registerPanelRoutes 注册 M3 面板路由（全部走鉴权 filter 默认保护）。
// 语义与蓝本 bridge.py/koubo.py 对齐；新增路由必须在此显式登记。
func registerPanelRoutes(srv *khttp.Server, d PanelDeps) {
	r := srv.Route("/")

	if d.Contentpkg != nil {
		registerDraftRoutes(srv, d.Contentpkg)
		r.POST("/api/v1/live-drafts/{id}/apply", func(c khttp.Context) error {
			var body struct {
				OwnerID          string `json:"owner_id"`
				FencingToken     int64  `json:"fencing_token"`
				ExpectedRevision int64  `json:"expected_revision"`
				PreflightID      string `json:"preflight_id"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if strings.TrimSpace(body.OwnerID) == "" || body.FencingToken <= 0 || body.ExpectedRevision <= 0 || strings.TrimSpace(body.PreflightID) == "" {
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_apply_request"})
			}
			journal, err := d.Contentpkg.Apply(c.Request().Context(), param(c, "id"), body.OwnerID, body.FencingToken, body.ExpectedRevision, body.PreflightID)
			if err != nil {
				return draftApplyError(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "journal": journal})
		})
	}

	if d.Effects != nil {
		r.GET("/api/v1/effects", func(c khttp.Context) error {
			effects, err := d.Effects.ListEffects(c.Request().Context(), c.Request().URL.Query().Get("status"))
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "effects": effects, "count": len(effects)})
		})
	}
	if d.InteractionDispatcher != nil {
		r.POST("/api/v1/effects/{id}/reconcile", func(c khttp.Context) error {
			var body struct {
				Action    interaction.ReconcileAction `json:"action"`
				ReceiptID string                      `json:"receipt_id"`
				Reason    string                      `json:"reason"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if err := d.InteractionDispatcher.Reconcile(c.Request().Context(), param(c, "id"), body.Action, body.ReceiptID, body.Reason); err != nil {
				if errors.Is(err, interaction.ErrInvalidReconcile) {
					return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "invalid_reconcile"})
				}
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "id": param(c, "id")})
		})
	}

	if d.TTS != nil {
		r.GET("/api/v1/tts/voices", func(c khttp.Context) error {
			cur, base, list := d.TTS.Voices(c.Request().Context())
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "current": cur, "active_base": base, "voices": list, "source": d.TTS.Source()})
		})
		r.POST("/api/v1/tts/voice", func(c khttp.Context) error {
			var body struct {
				Voice string `json:"voice"`
				Name  string `json:"name"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			name := body.Voice
			if name == "" {
				name = body.Name
			}
			if err := d.TTS.SetVoice(c.Request().Context(), name); err != nil {
				return mapTTSErr(c, err)
			}
			cur, _, list := d.TTS.Voices(c.Request().Context())
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "voice": cur, "voices": list})
		})
		r.POST("/api/v1/tts/preview", func(c khttp.Context) error {
			var body struct {
				Text  string `json:"text"`
				Voice string `json:"voice"`
			}
			_ = decodeJSONBody(c, &body)
			if body.Text == "" {
				body.Text = c.Request().URL.Query().Get("text")
			}
			if body.Voice == "" {
				body.Voice = c.Request().URL.Query().Get("voice")
			}
			wav, err := d.TTS.Preview(c.Request().Context(), body.Text, body.Voice)
			if err != nil {
				return mapTTSErr(c, err)
			}
			c.Response().Header().Set("Content-Type", "audio/wav")
			c.Response().Header().Set("Content-Length", strconv.Itoa(len(wav)))
			_, err = c.Response().Write(wav)
			return err
		})
		r.GET("/api/v1/tts/files", func(c khttp.Context) error {
			// 只返回相对文件名，绝不向客户端暴露服务器绝对目录。
			_, files, err := d.TTS.Files()
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "files": files, "count": len(files)})
		})
		r.POST("/api/v1/tts/upload", func(c khttp.Context) error {
			saved, err := saveMultipart(c, d.TTS.Upload, d.TTS.Delete)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "files": saved, "count": len(saved)})
		})
		r.POST("/api/v1/tts/import-folder", func(c khttp.Context) error {
			var body struct {
				Path string `json:"path"`
				Dir  string `json:"dir"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if body.Path == "" {
				body.Path = body.Dir
			}
			// 白名单：只允许导入约定根目录之下的文件夹，越界 400。
			root := d.StorageRoot
			if root == "" {
				root = filepath.Dir(d.TTS.LibDir())
			}
			if !pathWithin(root, body.Path) {
				return badRequest(c, errors.New("path 越出允许的根目录"))
			}
			res, err := d.TTS.ImportFolder(body.Path)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, res)
		})
		r.POST("/api/v1/tts/delete", func(c khttp.Context) error {
			var body struct {
				FileName string `json:"file_name"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if err := d.TTS.Delete(body.FileName); err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "deleted": body.FileName})
		})
		r.GET("/api/v1/tts/queue", func(c khttp.Context) error {
			q := d.TTS.Queue()
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "queue": q})
		})
	}

	if d.Actions != nil {
		r.GET("/api/v1/actions/libraries", func(c khttp.Context) error {
			libs, err := d.Actions.Libraries(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "libraries": libs})
		})
		r.POST("/api/v1/actions/libraries", func(c khttp.Context) error {
			var body struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			lib, err := d.Actions.CreateLibrary(c.Request().Context(), body.Name, body.Description)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "library": lib})
		})
		r.DELETE("/api/v1/actions/libraries/{id}", func(c khttp.Context) error {
			if err := d.Actions.DeleteLibrary(c.Request().Context(), param(c, "id")); err != nil {
				return notFoundOrInternal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
		r.GET("/api/v1/actions", func(c khttp.Context) error {
			acts, err := d.Actions.Actions(c.Request().Context(), c.Request().URL.Query().Get("library_id"))
			if err != nil {
				return internal(c, err)
			}
			page, size, offset := pageQuery(c)
			paged := paginate(acts, offset, size)
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "actions": paged, "count": len(paged),
				"page": page, "page_size": size, "total": len(acts),
			})
		})
		r.POST("/api/v1/actions", func(c khttp.Context) error {
			var body struct {
				LibraryID   string `json:"library_id"`
				Name        string `json:"name"`
				Instruction string `json:"instruction"`
				Description string `json:"description"`
				DurationMS  int64  `json:"duration_ms"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if body.Instruction == "" {
				body.Instruction = body.Description
			}
			act, err := d.Actions.CreateAction(c.Request().Context(), body.LibraryID, body.Name, body.Instruction, body.DurationMS)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "action": act})
		})
		r.DELETE("/api/v1/actions/{id}", func(c khttp.Context) error {
			if err := d.Actions.DeleteAction(c.Request().Context(), param(c, "id")); err != nil {
				return notFoundOrInternal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
		r.GET("/api/v1/actions/queue", func(c khttp.Context) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "status": d.Actions.Scheduler().Status()})
		})
		r.POST("/api/v1/actions/send", func(c khttp.Context) error {
			var body struct {
				Text   string `json:"text"`
				Source string `json:"source"`
				User   string `json:"user"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if body.Source == "" {
				body.Source = "manual"
			}
			res, err := d.Actions.Scheduler().Command(c.Request().Context(), body.Text, body.Source, body.User)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "result": res})
		})
	}

	if d.Choreo != nil {
		r.GET("/api/v1/choreo", func(c khttp.Context) error {
			cfg, err := d.Choreo.Get()
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "config": cfg})
		})
		r.POST("/api/v1/choreo", func(c khttp.Context) error {
			var cfg choreo.Config
			if err := decodeJSONBody(c, &cfg); err != nil {
				return badRequest(c, err)
			}
			out, err := d.Choreo.Save(cfg)
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "config": out})
		})
		r.POST("/api/v1/choreo/groups", func(c khttp.Context) error {
			var g choreo.Group
			if err := decodeJSONBody(c, &g); err != nil {
				return badRequest(c, err)
			}
			cfg, err := d.Choreo.UpsertGroup(g)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "config": cfg})
		})
		r.DELETE("/api/v1/choreo/groups/{id}", func(c khttp.Context) error {
			cfg, err := d.Choreo.DeleteGroup(param(c, "id"))
			if err != nil {
				return notFoundOrInternal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "config": cfg})
		})
		r.POST("/api/v1/choreo/start", func(c khttp.Context) error {
			started := d.Choreo.Start(c.Request().Context())
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "started": started, "status": d.Choreo.Status()})
		})
		r.POST("/api/v1/choreo/stop", func(c khttp.Context) error {
			d.Choreo.Stop()
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "status": d.Choreo.Status()})
		})
		r.POST("/api/v1/choreo/run-once", func(c khttp.Context) error {
			sent, err := d.Choreo.RunOnce(c.Request().Context())
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "steps": sent, "status": d.Choreo.Status()})
		})
		r.GET("/api/v1/choreo/status", func(c khttp.Context) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "status": d.Choreo.Status()})
		})
	}

	if d.Koubo != nil {
		registerKouboRoutes(r, d.Koubo)
	}

	if d.Refs != nil {
		r.GET("/api/v1/prompt", func(c khttp.Context) error {
			st := d.Refs.State()
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": st, "source": st.Source})
		})
		r.POST("/api/v1/prompt", func(c khttp.Context) error {
			var body struct {
				OpType   string       `json:"op_type"`
				Images   []refs.Image `json:"images"`
				ImageIDs []string     `json:"image_ids"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			for _, id := range body.ImageIDs {
				body.Images = append(body.Images, refs.Image{ImageID: id})
			}
			op := strings.ToLower(strings.TrimSpace(body.OpType))
			if op == "" {
				if len(body.Images) == 0 {
					op = "remove"
				} else {
					op = "switch"
				}
			}
			var (
				st  refs.State
				err error
			)
			if op == "remove" {
				st, err = d.Refs.Remove(c.Request().Context())
			} else if op == "switch" {
				st, err = d.Refs.Switch(c.Request().Context(), body.Images)
			} else {
				return badRequest(c, errors.New("op_type 只支持 switch/remove"))
			}
			if err != nil {
				if errors.Is(err, refs.ErrTooManyImages) {
					return badRequest(c, err)
				}
				return c.JSON(http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error(), "state": st, "type": 14})
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": st})
		})
		r.POST("/api/v1/prompt/type14", func(c khttp.Context) error {
			var body struct {
				Error string `json:"error"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			d.Refs.NoteType14(c.Request().Context(), body.Error)
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": d.Refs.State()})
		})
		// F33 本地 refs 文件列表（蓝本 bridge.py:1348 GET /api/refs → {dir, items:[{name,size}]}）。
		r.GET("/api/v1/refs", func(c khttp.Context) error {
			items, err := d.Refs.ListFiles()
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "items": items})
		})
		// F33 上传（multipart file 字段；png/jpeg ≤10MB ≤4096px，校验顺序对齐 avatar §5.3）。
		r.POST("/api/v1/refs/upload", func(c khttp.Context) error {
			if err := c.Request().ParseMultipartForm(avatar.MaxImageBytes + 1); err != nil {
				return badRequest(c, err)
			}
			if c.Request().MultipartForm == nil {
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "file_required"})
			}
			fh := c.Request().MultipartForm.File["file"]
			if len(fh) == 0 {
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "file_required"})
			}
			f, err := fh[0].Open()
			if err != nil {
				return badRequest(c, err)
			}
			defer func() { _ = f.Close() }()
			saved, err := d.Refs.SaveUpload(fh[0].Filename, f)
			if err != nil {
				return mapRefUploadErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "file": saved})
		})
	}

	if d.Songs != nil {
		r.GET("/api/v1/songs", func(c khttp.Context) error {
			files, err := d.Songs.List()
			if err != nil {
				return internal(c, err)
			}
			page, size, offset := pageQuery(c)
			paged := paginate(files, offset, size)
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "files": paged, "count": len(paged),
				"page": page, "page_size": size, "total": len(files),
			})
		})
		r.POST("/api/v1/songs/upload", func(c khttp.Context) error {
			saved, err := saveMultipart(c, d.Songs.Upload, d.Songs.Delete)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "files": saved, "count": len(saved)})
		})
		r.POST("/api/v1/songs/play", func(c khttp.Context) error {
			var body struct {
				FileName string `json:"file_name"`
				Script   string `json:"script"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			st, err := d.Songs.Play(c.Request().Context(), body.FileName, body.Script)
			if err != nil {
				return mapSongErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": st})
		})
		r.POST("/api/v1/songs/stop", func(c khttp.Context) error {
			st, err := d.Songs.Stop(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": st})
		})
		r.DELETE("/api/v1/songs/{name}", func(c khttp.Context) error {
			name, _ := url.PathUnescape(param(c, "name"))
			if err := d.Songs.Delete(name); err != nil {
				return notFoundOrInternal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "deleted": name})
		})
		r.GET("/api/v1/songs/status", func(c khttp.Context) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": d.Songs.Status()})
		})
	}

	if d.SongRequests != nil {
		r.GET("/api/v1/song-requests", func(c khttp.Context) error {
			requests, err := d.SongRequests.List(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "requests": requests, "count": len(requests)})
		})
		changeStatus := func(c khttp.Context, action string) error {
			ctx := c.Request().Context()
			id := param(c, "id")
			if err := d.SongRequests.ChangeStatus(ctx, id, action); err != nil {
				switch {
				case errors.Is(err, song.ErrRequestNotFound):
					return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "song_request_not_found"})
				case errors.Is(err, song.ErrInvalidRequestTransition):
					return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "invalid_song_request_transition"})
				default:
					return internal(c, err)
				}
			}
			req, _, err := d.SongRequests.Get(ctx, id)
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "request": req})
		}
		r.POST("/api/v1/song-requests/{id}/approve", func(c khttp.Context) error {
			return changeStatus(c, song.RequestActionApprove)
		})
		r.POST("/api/v1/song-requests/{id}/reject", func(c khttp.Context) error {
			return changeStatus(c, song.RequestActionReject)
		})
		r.POST("/api/v1/song-requests/{id}/cancel", func(c khttp.Context) error {
			return changeStatus(c, song.RequestActionCancel)
		})
	}

	if d.Archive != nil {
		r.GET("/api/v1/sessions", func(c khttp.Context) error {
			page, size, offset := pageQuery(c)
			phase := c.Request().URL.Query().Get("phase")
			rows, err := d.Archive.List(c.Request().Context(), size, offset, phase)
			if err != nil {
				return internal(c, err)
			}
			total, err := d.Archive.Count(c.Request().Context(), phase)
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "sessions": rows, "count": len(rows),
				"page": page, "page_size": size, "total": total,
			})
		})
		r.GET("/api/v1/sessions/{id}", func(c khttp.Context) error {
			include := c.Request().URL.Query().Get("include") != "summary"
			detail, err := d.Archive.Detail(c.Request().Context(), param(c, "id"), include)
			if err != nil {
				return notFoundOrInternal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "session": detail.Session, "cost": detail.Cost, "events": detail.Events, "event_count": detail.EventN})
		})
		r.GET("/api/v1/sessions/{id}/export", func(c khttp.Context) error {
			format := c.Request().URL.Query().Get("format")
			raw, filename, ctype, err := d.Archive.Export(c.Request().Context(), param(c, "id"), format)
			if err != nil {
				return notFoundOrInternal(c, err)
			}
			c.Response().Header().Set("Content-Type", ctype)
			c.Response().Header().Set("Content-Disposition", "attachment; filename="+filename)
			_, err = c.Response().Write(raw)
			return err
		})
	}

	if d.Billing != nil {
		r.GET("/api/v1/cost/ledger/{id}", func(c khttp.Context) error {
			l, ok, err := d.Billing.Get(c.Request().Context(), param(c, "id"))
			if err != nil {
				return internal(c, err)
			}
			if !ok {
				return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "ledger_not_found"})
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "ledger": l})
		})
		r.POST("/api/v1/cost/ledger", func(c khttp.Context) error {
			var l billing.Ledger
			if err := decodeJSONBody(c, &l); err != nil {
				return badRequest(c, err)
			}
			out, err := d.Billing.Upsert(c.Request().Context(), l)
			if err != nil {
				return badRequest(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "ledger": out})
		})
		r.GET("/api/v1/cost/summary", func(c khttp.Context) error {
			groupBy := c.Request().URL.Query().Get("group_by")
			totals, err := d.Billing.Summary(c.Request().Context(), groupBy)
			if err != nil {
				return internal(c, err)
			}
			if groupBy == "" {
				groupBy = "session"
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "group_by": groupBy, "totals": totals,
				"note": "credits/tokens 来自实测；estimated_cost 由调用方提供，标记 estimated=true 时仅为估算",
			})
		})
	}

	// F34/N2 录播（蓝本 bridge.py:408-434）：GET 开关+引擎状态；POST 保存开关（下一场生效）；
	// POST /fetch 手动再拉一场出片（异步，立即返回）。
	if d.Settings != nil {
		// GET /api/v1/record → {enabled, state, url?, live_id?}（蓝本 {enabled, record_dir, state}）。
		r.GET("/api/v1/record", func(c khttp.Context) error {
			enabled, err := recordEnabled(c.Request().Context(), d.Settings)
			if err != nil {
				return internal(c, err)
			}
			resp := map[string]any{"ok": true, "enabled": enabled, "state": recording.StateUnavailable}
			if d.Recording != nil {
				resp["state"] = d.Recording.State()
				if u := d.Recording.URL(); u != "" {
					resp["url"] = u
				}
			}
			return c.JSON(http.StatusOK, resp)
		})
		// POST /api/v1/record {enabled} → 持久化 misc.record_enabled；开关只在建会话时生效。
		r.POST("/api/v1/record", func(c khttp.Context) error {
			var body struct {
				Enabled bool `json:"enabled"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if err := d.Settings.Patch(c.Request().Context(), "misc", map[string]any{"record_enabled": body.Enabled}); err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "enabled": body.Enabled,
				"note": "已保存；本场已连线的话，下一场生效",
			})
		})
		// POST /api/v1/record/fetch {live_id?} → 手动再拉出片；不传 live_id 用最近一场。
		// 长轮询（最长 1800s）放 goroutine，立即返回 processing；dev/未开通 fail-closed 503。
		r.POST("/api/v1/record/fetch", func(c khttp.Context) error {
			if d.Recording == nil || !d.Recording.Enabled() {
				return c.JSON(http.StatusServiceUnavailable, map[string]any{
					"ok": false, "error": "recording_unavailable",
				})
			}
			var body struct {
				LiveID string `json:"live_id"`
			}
			_ = decodeJSONBody(c, &body) // 空 body 合法（用最近一场）
			go func() {
				if _, err := d.Recording.OnSessionStop(context.Background(), body.LiveID); err != nil {
					slog.Warn("record fetch 失败", "err", err)
				}
			}()
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "live_id": body.LiveID, "state": recording.StateProcessing,
			})
		})
	}

	// F08/F36/G03/N3 额度实时查询（蓝本 bridge.py:1508 /api/credits 透传 vidu credits）。
	// 无凭据/未装配 → 200 明确空态（configured=false）；Vidu 错误 → 502（对齐蓝本）。
	r.GET("/api/v1/credits", func(c khttp.Context) error {
		if d.Credits == nil {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "configured": false, "remain": nil})
		}
		raw, err := d.Credits(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		}
		// 透传 Vidu 原始字段（含 remain_credit），附归一化 remain 便于前端直读。
		resp := map[string]any{"ok": true, "configured": true}
		for k, v := range raw {
			resp[k] = v
		}
		if _, has := resp["remain"]; !has {
			if rc, ok := raw["remain_credit"]; ok {
				resp["remain"] = rc
			}
		}
		return c.JSON(http.StatusOK, resp)
	})

	if d.Danmaku != nil {
		r.GET("/api/v1/danmaku/state", func(c khttp.Context) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": d.Danmaku.State(time.Now()), "top_scope": "calendar_day"})
		})
	}

	if d.Patrons != nil {
		// 蓝本 bridge.py:878 GET /api/patrons：名单 + 状态（welcomed/count）。
		r.GET("/api/v1/patrons", func(c khttp.Context) error {
			items, err := d.Patrons.List(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "items": items, "state": d.Patrons.State(c.Request().Context()),
			})
		})
		// 蓝本 bridge.py:884 GET /api/patrons/ids：阅读端本地匹配 join 用。
		r.GET("/api/v1/patrons/ids", func(c khttp.Context) error {
			nicks, uids, err := d.Patrons.IDs(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "nicks": nicks, "uids": uids})
		})
		// 蓝本 bridge.py:891 POST /api/patrons/save：全量替换名单。
		r.POST("/api/v1/patrons/save", func(c khttp.Context) error {
			var body struct {
				Items []patron.Entry `json:"items"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			n, err := d.Patrons.Save(c.Request().Context(), body.Items)
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "count": n})
		})
		// 蓝本 bridge.py:912 POST /api/patron/join：贵宾进场欢迎（未开播静默 skipped）。
		r.POST("/api/v1/patron/join", func(c khttp.Context) error {
			var body struct {
				Nick string `json:"nick"`
				UID  string `json:"uid"`
				Note string `json:"note"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			line, ok := d.Patrons.OnJoin(c.Request().Context(), body.Nick, body.UID, body.Note)
			if !ok {
				skipped := "not_patron"
				if !d.Patrons.IsLive() {
					skipped = "idle"
				}
				return c.JSON(http.StatusOK, map[string]any{"ok": true, "skipped": skipped})
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "line": line})
		})
		// 蓝本 bridge.py:926 POST /api/patron/welcome：强制欢迎（未开播 409）。
		r.POST("/api/v1/patron/welcome", func(c khttp.Context) error {
			var body struct {
				Nick string `json:"nick"`
				UID  string `json:"uid"`
				Note string `json:"note"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if !d.Patrons.IsLive() {
				return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "not_live"})
			}
			line, ok := d.Patrons.Welcome(c.Request().Context(), body.Nick, body.Note)
			if !ok {
				return c.JSON(http.StatusOK, map[string]any{"ok": true, "skipped": "already_welcomed"})
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "line": line})
		})
	}

	if d.Personas != nil {
		// 蓝本 bridge.py:1090 GET /api/personas：items + active + 当前人设完整 prompt。
		r.GET("/api/v1/personas", func(c khttp.Context) error {
			items, active, err := d.Personas.List(c.Request().Context())
			if err != nil {
				return internal(c, err)
			}
			prompt := ""
			for _, p := range items {
				if p.ID == active {
					prompt = p.SystemPrompt
					break
				}
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "active": active, "items": items,
				"prompt": prompt, "prompt_chars": len([]rune(prompt)),
				"storage": d.Personas.Storage(),
			})
		})
		// 蓝本 bridge.py:1105 personas_select：404 校验 + 切换后立即清记忆（G04，消除 restart_required）。
		r.POST("/api/v1/personas/select", func(c khttp.Context) error {
			var body struct {
				ID string `json:"id"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			p, err := d.Personas.Select(c.Request().Context(), body.ID)
			if err != nil {
				return mapPersonaErr(c, err)
			}
			cleared := 0
			if d.ResetLLM != nil {
				cleared = d.ResetLLM(c.Request().Context())
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "active": p.ID, "name": p.Name,
				"chars": len([]rune(p.SystemPrompt)), "cleared_turns": cleared,
			})
		})
		// 蓝本 bridge.py:1122 personas_save：id/prompt 校验失败 400。
		r.POST("/api/v1/personas/save", func(c khttp.Context) error {
			var body struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Tagline      string `json:"tagline"`
				Voice        string `json:"voice"`
				VoiceID      string `json:"voice_id"`
				AvatarNote   string `json:"avatar_note"`
				SystemPrompt string `json:"system_prompt"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			voice := body.Voice
			if voice == "" {
				voice = body.VoiceID
			}
			p, err := d.Personas.Save(c.Request().Context(), body.ID, persona.SaveInput{
				Name:         body.Name,
				Tagline:      body.Tagline,
				VoiceID:      voice,
				AvatarNote:   body.AvatarNote,
				SystemPrompt: body.SystemPrompt,
			})
			if err != nil {
				return mapPersonaErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "persona": p})
		})
		// 蓝本 bridge.py:1132 personas_delete：删 active 409，不存在 404。
		r.POST("/api/v1/personas/delete", func(c khttp.Context) error {
			var body struct {
				ID string `json:"id"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if err := d.Personas.Delete(c.Request().Context(), body.ID); err != nil {
				return mapPersonaErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "deleted": body.ID})
		})
	}

	// N1 管理员对话调试（蓝本 bridge.py:469 /api/chat → engine.chat）：
	// 发文本走 chat.Manager（admin 独立记忆 + 人设），应答经 sink 上屏播报并返回 reply。
	// 蓝本语义：无进行中会话 409；LLM 未配置 fail-closed 503。
	if d.Chat != nil {
		r.POST("/api/v1/chat", func(c khttp.Context) error {
			var body struct {
				Text    string `json:"text"`
				Content string `json:"content"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			text := strings.TrimSpace(body.Text)
			if text == "" {
				text = strings.TrimSpace(body.Content)
			}
			if text == "" {
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "text_required"})
			}
			reply, err := d.Chat(c.Request().Context(), text)
			if err != nil {
				return mapChatErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "reply": reply})
		})
	}

	// F42/G18 Vidu 多 key 管理（蓝本 bridge.py:1458 GET、1476 POST /api/vidu/keys）。
	if d.ViduKeys != nil {
		// GET：首访把当前生效 key 落库为 active（蓝本行 1458-1465）；响应只带尾 8 位。
		r.GET("/api/v1/vidu/keys", func(c khttp.Context) error {
			ctx := c.Request().Context()
			if d.ViduKeyCurrent != nil {
				if _, err := d.ViduKeys.Adopt(ctx, d.ViduKeyCurrent()); err != nil {
					return internal(c, err)
				}
			}
			active, keys, err := d.ViduKeys.List(ctx)
			if err != nil {
				return internal(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "active": active, "keys": keys})
		})
		// POST：action=add|activate|delete（蓝本单端点分发）。
		r.POST("/api/v1/vidu/keys", func(c khttp.Context) error {
			var body struct {
				Action   string `json:"action"`
				Key      string `json:"key"`
				Activate bool   `json:"activate"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			ctx := c.Request().Context()
			switch strings.ToLower(strings.TrimSpace(body.Action)) {
			case "add":
				entry, err := d.ViduKeys.Add(ctx, body.Key, body.Activate)
				if err != nil {
					return mapViduKeysErr(c, err)
				}
				return c.JSON(http.StatusOK, map[string]any{"ok": true, "key": entry})
			case "activate":
				if err := d.ViduKeys.Activate(ctx, body.Key); err != nil {
					return mapViduKeysErr(c, err)
				}
				active, keys, err := d.ViduKeys.List(ctx)
				if err != nil {
					return internal(c, err)
				}
				return c.JSON(http.StatusOK, map[string]any{"ok": true, "active": active, "keys": keys})
			case "delete":
				if err := d.ViduKeys.Delete(ctx, body.Key); err != nil {
					return mapViduKeysErr(c, err)
				}
				active, keys, err := d.ViduKeys.List(ctx)
				if err != nil {
					return internal(c, err)
				}
				return c.JSON(http.StatusOK, map[string]any{"ok": true, "active": active, "keys": keys})
			default:
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown_action"})
			}
		})
	}

	// G 批 M2 数字人档案 11 RPC + 图片路由（v2 §3.1）；追加式，不改既有 handler。
	// 鉴权：filter 已强制 Bearer（含 GET），handler 内不做冗余鉴权（v3 §5 论证）。
	if d.Avatars != nil {
		// #1 ListAvatars：分页 + storage 标识 + include_deleted。
		r.GET("/api/v1/avatars", func(c khttp.Context) error {
			_, size, offset := pageQuery(c)
			includeDeleted := c.Request().URL.Query().Get("include_deleted") == "true"
			items, total, err := d.Avatars.List(c.Request().Context(), size, offset, includeDeleted)
			if err != nil {
				return internal(c, err)
			}
			out := make([]map[string]any, 0, len(items))
			for _, a := range items {
				out = append(out, avatarListItem(a))
			}
			return c.JSON(http.StatusOK, map[string]any{
				"ok": true, "avatars": out, "total": total, "storage": d.Avatars.Storage(),
			})
		})
		// #3 CreateAvatar → 201。
		r.POST("/api/v1/avatars", func(c khttp.Context) error {
			var body struct {
				Name            string `json:"name"`
				Slug            string `json:"slug"`
				SystemPrompt    string `json:"system_prompt"`
				Greeting        string `json:"greeting"`
				VoiceID         string `json:"voice_id"`
				ActionLibraryID string `json:"action_library_id"`
				ImageAssetID    string `json:"image_asset_id"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			a, err := d.Avatars.Create(c.Request().Context(), avatar.CreateInput{
				Name: body.Name, Slug: body.Slug, SystemPrompt: body.SystemPrompt,
				Greeting: body.Greeting, VoiceID: body.VoiceID,
				ActionLibraryID: body.ActionLibraryID, ImageAssetID: body.ImageAssetID,
			})
			if err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusCreated, map[string]any{"ok": true, "avatar": avatarDetail(a)})
		})
		// #2 GetAvatar。
		r.GET("/api/v1/avatars/{id}", func(c khttp.Context) error {
			a, err := d.Avatars.Get(c.Request().Context(), param(c, "id"))
			if err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "avatar": avatarDetail(a)})
		})
		// #4 UpdateAvatar：body.revision 乐观锁（CAS），撞 → 409 revision_conflict。
		r.PUT("/api/v1/avatars/{id}", func(c khttp.Context) error {
			var body struct {
				Name            string `json:"name"`
				Slug            string `json:"slug"`
				SystemPrompt    string `json:"system_prompt"`
				Greeting        string `json:"greeting"`
				VoiceID         string `json:"voice_id"`
				ActionLibraryID string `json:"action_library_id"`
				ImageAssetID    string `json:"image_asset_id"`
				Revision        int64  `json:"revision"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			a, err := d.Avatars.Update(c.Request().Context(), param(c, "id"), avatar.UpdateInput{
				Name: body.Name, Slug: body.Slug, SystemPrompt: body.SystemPrompt,
				Greeting: body.Greeting, VoiceID: body.VoiceID,
				ActionLibraryID: body.ActionLibraryID, ImageAssetID: body.ImageAssetID,
			}, body.Revision)
			if err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "avatar": avatarDetail(a)})
		})
		// #5 DeleteAvatar：软删；当前 active → 409 avatar_in_use。
		r.POST("/api/v1/avatars/{id}/delete", func(c khttp.Context) error {
			id := param(c, "id")
			if err := d.Avatars.Delete(c.Request().Context(), id); err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "deleted": id})
		})
		// #6 DuplicateAvatar：slug 递增 -copy/-copy2…；vidu_* 清空待重同步 → 201。
		r.POST("/api/v1/avatars/{id}/duplicate", func(c khttp.Context) error {
			a, err := d.Avatars.Duplicate(c.Request().Context(), param(c, "id"))
			if err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusCreated, map[string]any{"ok": true, "avatar": avatarDetail(a)})
		})
		// #7 SetActiveAvatar：排他置 active。
		r.POST("/api/v1/avatars/{id}/activate", func(c khttp.Context) error {
			if err := d.Avatars.SetActive(c.Request().Context(), param(c, "id")); err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
		// #9 SyncAvatarToVidu —— M2 仅路由占位（501）。
		// M3 已落地：202 异步任务化（入队即返 202 {job_id, state:"queued"}；
		// 幂等复放 200：进行中复放当前任务、sha256 未变复放 dedup:true；
		// ListAvatars/GetAvatar 的 vidu_sync_state 轮询闭环，v3 §2.2）。
		r.POST("/api/v1/avatars/{id}/sync-vidu", func(c khttp.Context) error {
			if d.AvatarSync == nil {
				return c.JSON(http.StatusServiceUnavailable,
					map[string]any{"ok": false, "error": "vidu_key_unconfigured", "detail": "sync-vidu 需要 vidu.engine=real 且配置 api_key"})
			}
			job, created, dedup, err := d.AvatarSync.Start(c.Request().Context(), param(c, "id"))
			if err != nil {
				return mapAvatarErr(c, err)
			}
			code := http.StatusOK
			if created {
				code = http.StatusAccepted
			}
			body := map[string]any{"ok": true, "job_id": job.ID, "state": job.State}
			if dedup {
				body["dedup"] = true
			}
			return c.JSON(code, body)
		})
		// #11 RemoveAvatarRef：解绑参考图，剩余 ord 重排。
		r.POST("/api/v1/avatars/{id}/refs/remove", func(c khttp.Context) error {
			var body struct {
				AssetID string `json:"asset_id"`
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if body.AssetID == "" {
				return badRequest(c, errors.New("asset_id 必填"))
			}
			if err := d.Avatars.RemoveRef(c.Request().Context(), param(c, "id"), body.AssetID); err != nil {
				return mapAvatarErr(c, err)
			}
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
		if d.AvatarImages != nil {
			// #8 UploadAvatarImage：multipart 上传（§5.3 校验链），dedup 复用同 sha256。
			r.POST("/api/v1/avatars/upload", func(c khttp.Context) error {
				return uploadAvatarImage(c, d.AvatarImages, "image", nil)
			})
			// #10 UploadAvatarRef：上传 + 绑定（ord 追加）；>3 → 400 too_many_refs。
			r.POST("/api/v1/avatars/{id}/refs", func(c khttp.Context) error {
				personaID := param(c, "id")
				return uploadAvatarImage(c, d.AvatarImages, "ref", func(a avatar.Asset) error {
					return d.Avatars.AddRef(c.Request().Context(), personaID, a.ID)
				})
			})
			// GetAvatarImage：读取时 http.DetectContentType 嗅探（v3 §5.2，零 schema 变更）。
			r.GET("/api/v1/avatars/{id}/image", func(c khttp.Context) error {
				a, err := d.Avatars.Get(c.Request().Context(), param(c, "id"))
				if err != nil {
					return mapAvatarErr(c, err)
				}
				if a.ImageAsset.ID == "" {
					return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "avatar_image_not_found"})
				}
				return serveAvatarAsset(c, d.AvatarImages, a.ImageAsset.ID)
			})
			// GetAvatarAssetImage：refs[].url 落点（v2 §3.1 #2 refs[].url）。
			r.GET("/api/v1/avatars/{aid}/assets/{asset_id}/image", func(c khttp.Context) error {
				return serveAvatarAsset(c, d.AvatarImages, param(c, "asset_id"))
			})
		}
	}
}

// registerKouboRoutes 对应 koubo.py 的 14 条路由。
func registerKouboRoutes(r *khttp.Router, svc *koubo.Service) {
	stateJSON := func(c khttp.Context, st koubo.State, extra map[string]any) error {
		out := map[string]any{"ok": true, "state": st}
		for k, v := range extra {
			out[k] = v
		}
		return c.JSON(http.StatusOK, out)
	}
	r.GET("/api/v1/koubo/state", func(c khttp.Context) error {
		return stateJSON(c, svc.State(), nil)
	})
	r.POST("/api/v1/koubo/split", func(c khttp.Context) error {
		var body struct {
			Text     string `json:"text"`
			MaxChars int    `json:"max_chars"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.Split(body.Text, body.MaxChars)
		if err != nil {
			return badRequest(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/seg", func(c khttp.Context) error {
		var body struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.UpdateSegment(body.ID, body.Text)
		if err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/merge", func(c khttp.Context) error {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.Merge(body.IDs)
		if err != nil {
			return badRequest(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/seg/split", func(c khttp.Context) error {
		var body struct {
			ID      string `json:"id"`
			Pos     int    `json:"pos"`
			CharPos int    `json:"char_pos"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		if body.Pos == 0 {
			body.Pos = body.CharPos
		}
		st, err := svc.SplitSegment(body.ID, body.Pos)
		if err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/tts", func(c khttp.Context) error {
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.TTS(c.Request().Context(), body.ID)
		if err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/tts_all", func(c khttp.Context) error {
		var body struct {
			OnlyMissing *bool `json:"only_missing"`
		}
		_ = decodeJSONBody(c, &body)
		only := true
		if body.OnlyMissing != nil {
			only = *body.OnlyMissing
		}
		job, err := svc.TTSAll(c.Request().Context(), only)
		if err != nil {
			return internal(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "job": job})
	})
	r.POST("/api/v1/koubo/select", func(c khttp.Context) error {
		var body struct {
			ID   string `json:"id"`
			Take int    `json:"take"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.Select(body.ID, body.Take)
		if err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/action", func(c khttp.Context) error {
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		st, err := svc.Action(c.Request().Context(), body.ID)
		if err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/actions_all", func(c khttp.Context) error {
		st, err := svc.ActionsAll(c.Request().Context())
		if err != nil {
			return internal(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.POST("/api/v1/koubo/play", func(c khttp.Context) error {
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return badRequest(c, err)
		}
		if err := svc.Play(c.Request().Context(), body.ID); err != nil {
			return mapKouboErr(c, err)
		}
		return stateJSON(c, svc.State(), map[string]any{"playing": true})
	})
	r.POST("/api/v1/koubo/play_all", func(c khttp.Context) error {
		var body struct {
			GapMs int `json:"gap_ms"`
		}
		_ = decodeJSONBody(c, &body)
		ctx := c.Request().Context()
		go func() {
			_, _ = svc.PlayAll(ctx, body.GapMs)
		}()
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "started": true})
	})
	r.POST("/api/v1/koubo/stop", func(c khttp.Context) error {
		st, err := svc.Stop(c.Request().Context())
		if err != nil {
			return internal(c, err)
		}
		return stateJSON(c, st, nil)
	})
	r.GET("/api/v1/koubo/audio/{name}", func(c khttp.Context) error {
		name, _ := url.PathUnescape(param(c, "name"))
		p, err := svc.AudioPath(name)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "audio_not_found"})
		}
		http.ServeFile(c.Response(), c.Request(), p)
		return nil
	})
}

// ---------- helpers ----------

type uploadFunc func(name string, r io.Reader) (media.File, error)

// saveMultipart 保存 multipart 上传；单文件超过 50MB 拒绝，
// 任一文件失败时清理已保存的半成品，不留垃圾。
func saveMultipart(c khttp.Context, fn uploadFunc, del func(name string) error) ([]media.File, error) {
	if err := c.Request().ParseMultipartForm(maxUploadFileBytes); err != nil {
		return nil, err
	}
	if c.Request().MultipartForm == nil {
		return nil, errors.New("请求必须是 multipart/form-data")
	}
	saved := []media.File{}
	cleanup := func() {
		for _, item := range saved {
			_ = del(item.Name)
		}
	}
	for _, files := range c.Request().MultipartForm.File {
		for _, fh := range files {
			if fh.Size > maxUploadFileBytes {
				cleanup()
				return nil, errors.New("文件超过 50MB 上限: " + fh.Filename)
			}
			f, err := fh.Open()
			if err != nil {
				cleanup()
				return nil, err
			}
			item, err := fn(fh.Filename, io.LimitReader(f, maxUploadFileBytes+1))
			_ = f.Close()
			if err != nil {
				cleanup()
				return nil, err
			}
			if item.Size > maxUploadFileBytes {
				_ = del(item.Name)
				cleanup()
				return nil, errors.New("文件超过 50MB 上限: " + fh.Filename)
			}
			saved = append(saved, item)
		}
	}
	if len(saved) == 0 {
		return nil, errors.New("没有收到文件")
	}
	return saved, nil
}

// pageQuery 解析 page/page_size（默认 1/20，page_size 上限 100），返回 page/size/offset。
func pageQuery(c khttp.Context) (page, size, offset int) {
	page = intQuery(c, "page", 1)
	if page < 1 {
		page = 1
	}
	size = intQuery(c, "page_size", 20)
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size, (page - 1) * size
}

// paginate 对内存切片分页，越界返回空切片。
func paginate[T any](items []T, offset, size int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + size
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// pathWithin 判断 p 是否位于 root 目录之内（含 root 本身）。
func pathWithin(root, p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absP)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func decodeJSONBody(c khttp.Context, dst any) error {
	return json.NewDecoder(io.LimitReader(c.Request().Body, 1<<20)).Decode(dst)
}

func param(c khttp.Context, key string) string {
	if v := c.Vars(); v != nil {
		return v.Get(key)
	}
	return ""
}

func intQuery(c khttp.Context, key string, def int) int {
	raw := c.Request().URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func badRequest(c khttp.Context, err error) error {
	return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
}

// mapChatErr N1 对话路由错误映射（蓝本 engine.chat：无会话 RuntimeError → 409）：
// not_live → 409；LLM 未配置 fail-closed → 503；其余 LLM/播报失败 → 502。
func mapChatErr(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, ErrChatNotLive), errors.Is(err, voice.ErrNotLive):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "not_live"})
	case errors.Is(err, voice.ErrTurnActive):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "voice_turn_active"})
	case errors.Is(err, llm.ErrLLMNotConfigured):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "llm_not_configured"})
	default:
		return c.JSON(http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
	}
}

// ErrChatNotLive 无进行中会话时拒绝对话（对齐蓝本 engine.chat "没有进行中的会话" → 409）。
var ErrChatNotLive = errors.New("not_live")

// recordEnabled 读 misc.record_enabled 持久化开关（蓝本 config.rtc.managed_recording 语义）。
func recordEnabled(ctx context.Context, st *settings.Service) (bool, error) {
	groups, _, err := st.Get(ctx)
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		if g.ID == "misc" {
			v, _ := g.Values["record_enabled"].(bool)
			return v, nil
		}
	}
	return false, nil
}

// mapRefUploadErr F33 上传错误映射：校验失败 → 400 稳定错误码（与 avatar §5.3 同码）；目录未配置 → 503。
func mapRefUploadErr(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, refs.ErrRefNoDir):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, refs.ErrRefTooLarge), errors.Is(err, refs.ErrRefType),
		errors.Is(err, refs.ErrRefPixels), errors.Is(err, refs.ErrRefName):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
	default:
		return internal(c, err)
	}
}

// mapViduKeysErr F42 多 key 错误映射（蓝本 bridge.py:1488-1530）：
// 非法 key / 重复 → 400；找不到 → 404；删当前生效 → 409（任务拍板，蓝本为 400）；加密不可用 → 503。
func mapViduKeysErr(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, vidukeys.ErrInvalidKey), errors.Is(err, vidukeys.ErrDuplicate):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, vidukeys.ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, vidukeys.ErrActiveKey):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, vidukeys.ErrUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
	default:
		return internal(c, err)
	}
}

// internal 统一返回稳定错误码 internal_error；真实错误只进服务端日志，不外泄给客户端。
func internal(c khttp.Context, err error) error {
	if err != nil {
		logx.From(c.Request().Context()).Warn("internal error", "err", err.Error(), "path", c.Request().URL.Path)
	}
	return c.JSON(http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal_error"})
}

func notFoundOrInternal(c khttp.Context, err error) error {
	if errors.Is(err, archive.ErrNotFound) || errors.Is(err, action.ErrActionNotFound) ||
		errors.Is(err, action.ErrLibraryNotFound) || errors.Is(err, choreo.ErrNotFound) ||
		errors.Is(err, refs.ErrNoSignal) || errors.Is(err, song.ErrNotFound) {
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
	}
	return internal(c, err)
}

func mapTTSErr(c khttp.Context, err error) error {
	if errors.Is(err, tts.ErrUnavailable) {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "tts_unavailable", "message": "语音合成服务尚未配置，请先配置后再试听"})
	}
	if errors.Is(err, tts.ErrVoiceNotFound) || errors.Is(err, tts.ErrEmptyText) {
		return badRequest(c, err)
	}
	if errors.Is(err, tts.ErrQueueFull) {
		return c.JSON(http.StatusTooManyRequests, map[string]any{"ok": false, "error": err.Error()})
	}
	return internal(c, err)
}

func mapSongErr(c khttp.Context, err error) error {
	switch {
	// ErrNotLive 与 koubo 统一为 409：前置状态不满足，不是参数错误。
	case errors.Is(err, song.ErrNotLive):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, song.ErrEmptyName):
		return badRequest(c, err)
	case errors.Is(err, song.ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
	default:
		return internal(c, err)
	}
}

// mapPersonaErr 对齐蓝本：不存在 404、删 active 409、id/prompt 校验失败 400。
func mapPersonaErr(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, persona.ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, persona.ErrActiveConflict):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, persona.ErrInvalidID), errors.Is(err, persona.ErrEmptyPrompt):
		return badRequest(c, err)
	default:
		return internal(c, err)
	}
}

// avatarListItem ListAvatars 响应项（v2 §3.1 #1 键集 + avatar_asset_id：
// 前端「设为当前」预检依赖列表项直读主形象素材 id，与 detail 对齐）。
func avatarListItem(a avatar.Avatar) map[string]any {
	return map[string]any{
		"persona_id":      a.ID,
		"slug":            a.Slug,
		"name":            a.Name,
		"vidu_avatar_id":  a.ViduAvatarID,
		"vidu_image_uri":  a.ViduImageURI,
		"avatar_asset_id": a.ImageAsset.ID,
		"expires_at_ms":   a.ImageAsset.ExpiresAtMs,
		"vidu_sync_state": a.ViduSync,
		"is_active":       a.IsActive,
		"updated_at_ms":   a.UpdatedAtMs,
		"revision":        a.Revision,
	}
}

// avatarDetail GetAvatar/Create/Update/Duplicate 响应（v2 §3.1 #2：list 键集 + 详情键）。
func avatarDetail(a avatar.Avatar) map[string]any {
	m := avatarListItem(a)
	m["system_prompt"] = a.SystemPrompt
	m["greeting"] = a.Greeting
	m["voice_id"] = a.VoiceID
	m["action_library_id"] = a.ActionLibraryID
	refs := make([]map[string]any, 0, len(a.Refs))
	for _, ref := range a.Refs {
		refs = append(refs, map[string]any{
			"asset_id": ref.AssetID,
			"ord":      ref.Ord,
			"url": "/api/v1/avatars/" + url.PathEscape(a.ID) +
				"/assets/" + url.PathEscape(ref.AssetID) + "/image",
			"vidu_uri": ref.Asset.URI,
			"sha256":   ref.Asset.SHA256,
		})
	}
	m["refs"] = refs
	return m
}

// uploadAvatarImage 图片上传统一入口（#8/#10）：multipart file 字段 → §5.3 校验链
// → dedup 落盘登记 → 可选后置动作（#10 绑定 ref）。
func uploadAvatarImage(c khttp.Context, images *avatar.ImageService, kind string, after func(avatar.Asset) error) error {
	if err := c.Request().ParseMultipartForm(avatar.MaxImageBytes + (1 << 20)); err != nil {
		return badRequest(c, errors.New("请求必须是 multipart/form-data"))
	}
	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return badRequest(c, errors.New("file 必填（multipart/form-data）"))
	}
	defer func() { _ = file.Close() }()
	asset, dedup, err := images.SaveUpload(c.Request().Context(), kind, header.Filename, file)
	if err != nil {
		return mapAvatarErr(c, err)
	}
	if after != nil {
		if err := after(asset); err != nil {
			return mapAvatarErr(c, err)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"ok": true, "asset_id": asset.ID, "sha256": asset.SHA256, "dedup": dedup,
	})
}

// serveAvatarAsset 图片读取：路径只来自 DB（v3 §5.1），读取时 http.DetectContentType
// 嗅探（§5.2，mime 零 schema 变更），nosniff + 私有缓存头。
func serveAvatarAsset(c khttp.Context, images *avatar.ImageService, assetID string) error {
	p, err := images.ResolvePath(c.Request().Context(), assetID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "avatar_image_not_found"})
	}
	f, err := os.Open(p)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "avatar_image_not_found"})
	}
	head := make([]byte, 512)
	n, _ := f.Read(head)
	_ = f.Close()
	h := c.Response().Header()
	h.Set("Content-Type", http.DetectContentType(head[:n]))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeFile(c.Response(), c.Request(), p)
	return nil
}

// mapAvatarErr v2 §3.1 错误码：400 validation_failed/too_many_refs/图片校验，
// 404 avatar_not_found，409 revision_conflict/avatar_in_use/slug_conflict。
func mapAvatarErr(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, avatar.ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "validation_failed", "detail": err.Error()})
	case errors.Is(err, avatar.ErrTooManyRefs):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "too_many_refs"})
	case errors.Is(err, avatar.ErrImageTooLarge), errors.Is(err, avatar.ErrImageType), errors.Is(err, avatar.ErrImagePixels):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, avatar.ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "avatar_not_found"})
	case errors.Is(err, avatar.ErrViduKeyUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "vidu_key_unconfigured"})
	case errors.Is(err, avatar.ErrSyncImageRequired):
		return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "avatar_image_required"})
	case errors.Is(err, avatar.ErrRevisionConflict):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "revision_conflict"})
	case errors.Is(err, avatar.ErrAvatarInUse):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "avatar_in_use"})
	case errors.Is(err, avatar.ErrSlugConflict):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "slug_conflict"})
	default:
		return internal(c, err)
	}
}

func mapKouboErr(c khttp.Context, err error) error {
	switch {
	// ErrNotLive/ErrNoSelection 都是“前置状态不满足”，统一 409（与 song 对齐）。
	case errors.Is(err, koubo.ErrNotLive), errors.Is(err, koubo.ErrNoSelection):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
	case errors.Is(err, koubo.ErrSegmentGone), errors.Is(err, koubo.ErrInvalidSplit):
		return badRequest(c, err)
	default:
		return internal(c, err)
	}
}
