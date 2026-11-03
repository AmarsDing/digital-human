package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/biz/tts"
	"github.com/teamwork/digital-human/pkg/logx"
)

// maxUploadFileBytes 单文件上传上限。
const maxUploadFileBytes = 50 << 20 // 50MB

// PanelDeps 是 M3 面板后端的依赖集合；nil 服务会被跳过注册，便于渐进接线。
type PanelDeps struct {
	TTS     *tts.Service
	Actions *action.Service
	Choreo  *choreo.Service
	Koubo   *koubo.Service
	Refs    *refs.Service
	Songs   *song.Service
	Archive *archive.Service
	Billing *billing.Service
	Danmaku *danmaku.Service
	// StorageRoot 是 import-folder 允许的白名单根目录；为空时回退到 TTS 素材库所在根。
	StorageRoot string
}

// registerPanelRoutes 注册 M3 面板路由（全部走鉴权 filter 默认保护）。
// 语义与蓝本 bridge.py/koubo.py 对齐；新增路由必须在此显式登记。
func registerPanelRoutes(srv *khttp.Server, d PanelDeps) {
	r := srv.Route("/")

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
			}
			if err := decodeJSONBody(c, &body); err != nil {
				return badRequest(c, err)
			}
			if body.Instruction == "" {
				body.Instruction = body.Description
			}
			act, err := d.Actions.CreateAction(c.Request().Context(), body.LibraryID, body.Name, body.Instruction)
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

	if d.Danmaku != nil {
		r.GET("/api/v1/danmaku/state", func(c khttp.Context) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "state": d.Danmaku.State(time.Now()), "top_scope": "calendar_day"})
		})
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
