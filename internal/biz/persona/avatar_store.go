package persona

import (
	"context"
	"errors"

	"github.com/teamwork/digital-human/internal/biz/avatar"
)

// AvatarBackedStore persona.Store 的兼容壳实现（v3 §1.2 拍板：avatar 档案服务为
// Persona 行唯一写方；F 批 personas 四路由保留为兼容壳，存储层委托 avatar.AvatarStore）。
//
// 映射纪律：
//   - 人设壳字段（name/tagline/voice_id/avatar_note/system_prompt/is_active）直映射读写；
//   - 档案字段（vidu_*/refs/greeting/action_library_id/revision/软删）由 avatar 侧独占，
//     Save 走读改写保留，兼容壳不得抹除；
//   - 既有行 slug 由 avatar 档案身份独占，Save 不改写（新建行才用 p.Slug=id）；
//   - Delete 语义由物理删除变为软删（v1 永不物理删除），列表/Get 视角一致（不可见）。
type AvatarBackedStore struct {
	store avatar.AvatarStore
}

func NewAvatarBackedStore(store avatar.AvatarStore) *AvatarBackedStore {
	return &AvatarBackedStore{store: store}
}

func avatarToPersona(a avatar.Avatar) Persona {
	return Persona{
		ID:           a.ID,
		Slug:         a.Slug,
		Name:         a.Name,
		Tagline:      a.Tagline,
		VoiceID:      a.VoiceID,
		AvatarNote:   a.AvatarNote,
		SystemPrompt: a.SystemPrompt,
		IsActive:     a.IsActive,
		UpdatedAtMs:  a.UpdatedAtMs,
	}
}

func (s *AvatarBackedStore) Backend() string {
	if b, ok := s.store.(interface{ Backend() string }); ok {
		return b.Backend()
	}
	return "unknown"
}

func (s *AvatarBackedStore) List(ctx context.Context) ([]Persona, error) {
	items, _, err := s.store.List(ctx, 1000, 0, false)
	if err != nil {
		return nil, err
	}
	out := make([]Persona, 0, len(items))
	for _, a := range items {
		out = append(out, avatarToPersona(a))
	}
	return out, nil
}

func (s *AvatarBackedStore) Get(ctx context.Context, id string) (Persona, error) {
	a, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return Persona{}, err
	}
	if !ok {
		return Persona{}, ErrNotFound
	}
	return avatarToPersona(a), nil
}

// Save 按 ID upsert（保留 is_active 由 persona.Service 读旧值保证）；档案字段读改写保留。
func (s *AvatarBackedStore) Save(ctx context.Context, p Persona) error {
	cur, ok, err := s.store.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	if !ok {
		a := avatar.Avatar{
			ID:           p.ID,
			Slug:         p.Slug,
			Name:         p.Name,
			Tagline:      p.Tagline,
			VoiceID:      p.VoiceID,
			AvatarNote:   p.AvatarNote,
			SystemPrompt: p.SystemPrompt,
			IsActive:     p.IsActive,
			ViduSync:     avatar.ViduSyncState{State: "idle"},
		}
		if err := s.store.Create(ctx, a); err != nil {
			if errors.Is(err, avatar.ErrSlugConflict) {
				return ErrInvalidID
			}
			return err
		}
		return nil
	}
	// 更新路径：slug 保持档案身份不改写；CAS 撞 revision 重读重试一次。
	for attempt := 0; ; attempt++ {
		a := cur
		a.Name = p.Name
		a.Tagline = p.Tagline
		a.VoiceID = p.VoiceID
		a.AvatarNote = p.AvatarNote
		a.SystemPrompt = p.SystemPrompt
		a.IsActive = p.IsActive
		err := s.store.UpdateWithRevision(ctx, a, cur.Revision)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, avatar.ErrRevisionConflict) && attempt == 0:
			next, ok, gerr := s.store.Get(ctx, p.ID)
			if gerr != nil {
				return gerr
			}
			if !ok {
				return ErrNotFound
			}
			cur = next
		case errors.Is(err, avatar.ErrSlugConflict):
			return ErrInvalidID
		case errors.Is(err, avatar.ErrNotFound):
			return ErrNotFound
		default:
			return err
		}
	}
}

func (s *AvatarBackedStore) Delete(ctx context.Context, id string) error {
	if err := s.store.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, avatar.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *AvatarBackedStore) SetActive(ctx context.Context, id string) error {
	if err := s.store.SetActive(ctx, id); err != nil {
		if errors.Is(err, avatar.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *AvatarBackedStore) ActiveID(ctx context.Context) (string, error) {
	items, _, err := s.store.List(ctx, 1000, 0, false)
	if err != nil {
		return "", err
	}
	for _, a := range items {
		if a.IsActive {
			return a.ID, nil
		}
	}
	return "", nil
}
