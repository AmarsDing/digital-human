package avatar

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// ErrValidation 输入校验失败（v3 §9.1），server 层映射 400 validation_failed。
var ErrValidation = errors.New("validation_failed")

// slugRe v2 §3.1：^[a-z0-9-]{1,32}$。
var slugRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func validErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// CreateInput 新建档案入参（v2 §3.1 #3）。
type CreateInput struct {
	Name            string
	Slug            string // 空时由 name 派生（小写连字符），冲突走 store 语义
	SystemPrompt    string
	Greeting        string
	VoiceID         string
	ActionLibraryID string
	ImageAssetID    string
}

// UpdateInput 更新档案入参（v2 §3.1 #4）；vidu_*/refs/sync 状态不在更新面内，保持不变。
type UpdateInput struct {
	Name            string
	Slug            string
	SystemPrompt    string
	Greeting        string
	VoiceID         string
	ActionLibraryID string
	ImageAssetID    string // 非空时换绑主形象图；空保持现状
}

func validateProfile(name, slug, prompt, greeting string) error {
	if name == "" {
		return validErr("name 必填")
	}
	if len([]rune(name)) > 20 {
		return validErr("name 过长（≤20）")
	}
	if slug != "" && !slugRe.MatchString(slug) {
		return validErr("slug 非法（^[a-z0-9-]{1,32}$）")
	}
	if len([]rune(prompt)) > 2000 {
		return validErr("system_prompt 过长（≤2000）")
	}
	if len([]rune(greeting)) > 200 {
		return validErr("greeting 过长（≤200）")
	}
	return nil
}

// List 档案分页（十原语直转）。
func (s *Service) List(ctx context.Context, limit, offset int, includeDeleted bool) ([]Avatar, int, error) {
	return s.store.List(ctx, limit, offset, includeDeleted)
}

// Get 按 id 取档案；不存在或已软删 → ErrNotFound。
func (s *Service) Get(ctx context.Context, id string) (Avatar, error) {
	a, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return Avatar{}, err
	}
	if !ok {
		return Avatar{}, ErrNotFound
	}
	return a, nil
}

// Storage 存储后端标识（memory|ent）；store 未暴露时为空串。
func (s *Service) Storage() string {
	if b, ok := s.store.(interface{ Backend() string }); ok {
		return b.Backend()
	}
	return ""
}

// Create 新建档案（v2 §3.1 #3）：revision=1、is_active=false 由 store 语义保证。
func (s *Service) Create(ctx context.Context, in CreateInput) (Avatar, error) {
	if err := validateProfile(in.Name, in.Slug, in.SystemPrompt, in.Greeting); err != nil {
		return Avatar{}, err
	}
	slug := in.Slug
	if slug == "" {
		slug = deriveSlug(in.Name)
	}
	a := Avatar{
		ID:              uuid.NewString(),
		Slug:            slug,
		Name:            in.Name,
		SystemPrompt:    in.SystemPrompt,
		Greeting:        in.Greeting,
		VoiceID:         in.VoiceID,
		ActionLibraryID: in.ActionLibraryID,
		ViduSync:        ViduSyncState{State: "idle"},
	}
	if in.ImageAssetID != "" {
		asset, err := s.lookupAsset(ctx, in.ImageAssetID)
		if err != nil {
			return Avatar{}, err
		}
		a.ImageAsset = asset
	}
	if err := s.store.Create(ctx, a); err != nil {
		return Avatar{}, err
	}
	return s.Get(ctx, a.ID)
}

// Update CAS 更新（v2 §3.1 #4）：revision 撞 → ErrRevisionConflict；已软删 → ErrNotFound。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput, baseRevision int64) (Avatar, error) {
	if err := validateProfile(in.Name, in.Slug, in.SystemPrompt, in.Greeting); err != nil {
		return Avatar{}, err
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Avatar{}, err
	}
	cur.Name = in.Name
	if in.Slug != "" && in.Slug != cur.Slug {
		cur.Slug = in.Slug
	}
	cur.SystemPrompt = in.SystemPrompt
	cur.Greeting = in.Greeting
	cur.VoiceID = in.VoiceID
	cur.ActionLibraryID = in.ActionLibraryID
	if in.ImageAssetID != "" {
		asset, err := s.lookupAsset(ctx, in.ImageAssetID)
		if err != nil {
			return Avatar{}, err
		}
		cur.ImageAsset = asset
	}
	if err := s.store.UpdateWithRevision(ctx, cur, baseRevision); err != nil {
		return Avatar{}, err
	}
	return s.Get(ctx, id)
}

// Delete 软删（v2 §3.1 #5）；当前 active → ErrAvatarInUse（409）。
func (s *Service) Delete(ctx context.Context, id string) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if a.IsActive {
		return ErrAvatarInUse
	}
	return s.store.SoftDelete(ctx, id)
}

// Duplicate 复制档案（v2 §3.1 #6）：slug 递增 -copy/-copy2…；vidu_* 清空待重同步，
// revision 重置 1、is_active=false；refs 复制。
func (s *Service) Duplicate(ctx context.Context, id string) (Avatar, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return Avatar{}, err
	}
	dup := src
	dup.ID = uuid.NewString()
	dup.Revision = 1
	dup.IsActive = false
	dup.DeletedAtMs = 0
	dup.ViduAvatarID = ""
	dup.ViduImageURI = ""
	dup.ViduSync = ViduSyncState{State: "idle"}
	dup.Refs = nil
	var lastErr error
	for i := 1; i <= 100; i++ {
		slug := src.Slug + "-copy"
		if i > 1 {
			slug = fmt.Sprintf("%s-copy%d", src.Slug, i)
		}
		dup.Slug = slug
		dup.Name = src.Name + " 副本"
		if err := s.store.Create(ctx, dup); err != nil {
			if errors.Is(err, ErrSlugConflict) {
				lastErr = err
				continue
			}
			return Avatar{}, err
		}
		for _, r := range src.Refs {
			if err := s.store.AddRef(ctx, dup.ID, r.AssetID, r.Ord); err != nil {
				return Avatar{}, err
			}
		}
		return s.Get(ctx, dup.ID)
	}
	return Avatar{}, lastErr
}

// SetActive 排他置 active（v2 §3.1 #7）。
func (s *Service) SetActive(ctx context.Context, id string) error {
	return s.store.SetActive(ctx, id)
}

// AddRef 绑定参考图（v2 §3.1 #10）：ord 取下一槽位；>MaxRefs → ErrTooManyRefs。
func (s *Service) AddRef(ctx context.Context, personaID, assetID string) error {
	a, err := s.Get(ctx, personaID)
	if err != nil {
		return err
	}
	return s.store.AddRef(ctx, personaID, assetID, len(a.Refs))
}

// RemoveRef 解绑参考图（v2 §3.1 #11）；剩余 refs 由 store 重排 ord。
func (s *Service) RemoveRef(ctx context.Context, personaID, assetID string) error {
	return s.store.RemoveRef(ctx, personaID, assetID)
}

// lookupAsset 经素材登记端口取素材；store 未实现或素材不存在 → ErrNotFound。
func (s *Service) lookupAsset(ctx context.Context, assetID string) (Asset, error) {
	reg, ok := s.store.(AssetRegistry)
	if !ok {
		return Asset{}, ErrNotFound
	}
	a, ok, err := reg.GetAsset(ctx, assetID)
	if err != nil {
		return Asset{}, err
	}
	if !ok {
		return Asset{}, ErrNotFound
	}
	return a, nil
}

// deriveSlug name 派生 slug：小写字母/数字/连字符，非 [a-z0-9] 折叠为 -，截断 32。
func deriveSlug(name string) string {
	out := make([]rune, 0, 32)
	for _, r := range []rune(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			if n := len(out); n > 0 && out[n-1] != '-' {
				out = append(out, '-')
			}
		}
		if len(out) >= 32 {
			break
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "avatar"
	}
	return string(out)
}
