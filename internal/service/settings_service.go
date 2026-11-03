package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	kerrors "github.com/go-kratos/kratos/v2/errors"

	configv1 "github.com/teamwork/digital-human/api/gen/go/config/v1"
	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/settings"
)

type SettingsService struct {
	svc   *settings.Service
	dm    *danmaku.Service
	audit *audit.Service
}

func NewSettingsService(svc *settings.Service) *SettingsService { return &SettingsService{svc: svc} }

// SetDanmaku 注入弹幕服务，Reload 时把 danmaku.mode 真实 apply 到运行时。
func (s *SettingsService) SetDanmaku(dm *danmaku.Service) { s.dm = dm }

// SetAudit 注入审计服务，记录 Reload 的 apply 结果。
func (s *SettingsService) SetAudit(a *audit.Service) { s.audit = a }

func (s *SettingsService) Get(ctx context.Context, _ *configv1.GetSettingsRequest) (*configv1.GetSettingsReply, error) {
	groups, rev, err := s.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*configv1.SettingGroup, 0, len(groups))
	for _, g := range groups {
		raw, _ := json.Marshal(g.Values)
		out = append(out, &configv1.SettingGroup{Id: g.ID, Title: g.Title, ValuesJson: raw, Effective: g.Effective})
	}
	return &configv1.GetSettingsReply{Groups: out, Revision: rev}, nil
}

func (s *SettingsService) Save(ctx context.Context, req *configv1.SaveSettingsRequest) (*configv1.SaveSettingsReply, error) {
	groups := make([]settings.Group, 0, len(req.GetGroups()))
	for _, g := range req.GetGroups() {
		vals := map[string]any{}
		if len(g.GetValuesJson()) > 0 {
			if err := json.Unmarshal(g.GetValuesJson(), &vals); err != nil {
				return nil, kerrors.New(400, "settings_bad_values", "settings group "+g.GetId()+" values_json 不是合法 JSON")
			}
		}
		groups = append(groups, settings.Group{ID: g.GetId(), Values: vals})
	}
	rev, reload, err := s.svc.Save(ctx, groups, req.GetBaseRevision())
	if err != nil {
		switch {
		case errors.Is(err, settings.ErrRevisionConflict):
			return nil, kerrors.New(409, "settings_revision_conflict", err.Error())
		case errors.Is(err, settings.ErrValidation):
			return nil, kerrors.New(409, "settings_validation_failed", err.Error())
		default:
			// DB 等存储错误：按 5xx 上抛（kratos 默认映射为 500）。
			return nil, err
		}
	}
	return &configv1.SaveSettingsReply{Saved: true, Revision: rev, ReloadRequired: reload}, nil
}

// Reload 做真实 apply：至少把 danmaku.mode 推给 danmaku.Service，并留审计。
func (s *SettingsService) Reload(ctx context.Context, _ *configv1.ReloadSettingsRequest) (*configv1.ReloadSettingsReply, error) {
	groups, rev, err := s.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	applied := []string{}
	for _, g := range groups {
		if g.ID != "danmaku" || s.dm == nil {
			continue
		}
		if m, ok := g.Values["mode"].(string); ok && s.dm.SetMode(danmaku.Mode(m)) {
			applied = append(applied, "danmaku.mode="+m)
		}
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, audit.Entry{
			Actor: "local", Route: "/api/v1/settings/reload", Action: "RELOAD",
			Target: strings.Join(applied, ","),
		})
	}
	return &configv1.ReloadSettingsReply{Ok: true, Revision: rev}, nil
}
