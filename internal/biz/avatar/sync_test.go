package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeUploader 注入延迟与失败的 Vidu fake（SyncUploader 端口）。
type fakeUploader struct {
	mu          sync.Mutex
	uploads     int
	creates     int
	deletes     int
	uploadDelay time.Duration
	createErrs  []error // 依次消费：前 N 次 create 失败
}

func (f *fakeUploader) Upload(ctx context.Context, _ []byte, _ string) (string, error) {
	f.mu.Lock()
	f.uploads++
	n := f.uploads
	delay := f.uploadDelay
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(delay):
	}
	return fmt.Sprintf("ssupload:img-%d", n), nil
}

func (f *fakeUploader) CreateAvatar(context.Context, map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if len(f.createErrs) > 0 {
		err := f.createErrs[0]
		f.createErrs = f.createErrs[1:]
		return nil, err
	}
	return map[string]any{"avatar_id": fmt.Sprintf("va-%d", f.creates)}, nil
}

func (f *fakeUploader) DeleteAvatar(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return nil
}

func (f *fakeUploader) counts() (up, create, del int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads, f.creates, f.deletes
}

// newSyncFixture 内存存储 + 临时图片目录 + 已绑定主形象的档案。
func newSyncFixture(t *testing.T, up SyncUploader) (*SyncService, *MemoryAvatarStore, *ImageService, Avatar) {
	t.Helper()
	store := NewMemoryStore()
	images := NewImageService(store, t.TempDir())
	asset, dedup, err := images.SaveUpload(context.Background(), "image", "a.png", bytes.NewReader(makePNG(t, 8, 8)))
	if err != nil || dedup {
		t.Fatalf("SaveUpload = %v %v", err, dedup)
	}
	svc := NewService(store)
	a, err := svc.Create(context.Background(), CreateInput{
		Name: "小雅", Slug: "xiaoya", VoiceID: "v1", ImageAssetID: asset.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewSyncService(store, images, up, nil), store, images, a
}

// waitSyncState 轮询 vidu_sync_state 直到满足条件或超时（模拟前端 2s 轮询闭环）。
func waitSyncState(t *testing.T, store *MemoryAvatarStore, id string, want func(ViduSyncState) bool) ViduSyncState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, ok, err := store.Get(context.Background(), id)
		if err != nil || !ok {
			t.Fatalf("Get = %v %v", ok, err)
		}
		if want(a.ViduSync) {
			return a.ViduSync
		}
		time.Sleep(5 * time.Millisecond)
	}
	a, _, _ := store.Get(context.Background(), id)
	t.Fatalf("sync state 未收敛，当前 = %+v", a.ViduSync)
	return ViduSyncState{}
}

func TestSyncLifecycleDone(t *testing.T) {
	fake := &fakeUploader{}
	s, store, images, a := newSyncFixture(t, fake)
	ctx := context.Background()

	// 绑定一张参考图：refs 随 sync 一并同步（§3.1）。
	ref, _, err := images.SaveUpload(ctx, "ref", "r.png", bytes.NewReader(makePNG(t, 4, 4)))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewService(store).AddRef(ctx, a.ID, ref.ID); err != nil {
		t.Fatal(err)
	}

	job, created, dedup, err := s.Start(ctx, a.ID)
	if err != nil || !created || dedup {
		t.Fatalf("Start = created=%v dedup=%v err=%v", created, dedup, err)
	}
	if job.State != SyncStateQueued || job.ID == "" {
		t.Fatalf("job = %+v", job)
	}

	st := waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateDone })
	if st.JobID != job.ID || st.AssetSHA256 != a.ImageAsset.SHA256 || st.ViduAvatarID != "" {
		t.Fatalf("done state = %+v", st)
	}
	done, _, _ := store.Get(ctx, a.ID)
	if done.ViduAvatarID != "va-1" || done.ViduImageURI == "" {
		t.Fatalf("persisted vidu fields = %+v", done)
	}
	if done.ImageAsset.URI == "" || done.ImageAsset.ExpiresAtMs == 0 {
		t.Fatalf("主形象 Asset URI/expires 未回填 = %+v", done.ImageAsset)
	}
	if len(done.Refs) != 1 || done.Refs[0].Asset.URI == "" {
		t.Fatalf("ref URI 未回填 = %+v", done.Refs)
	}
	if up, creates, _ := fake.counts(); up != 2 || creates != 1 {
		t.Fatalf("uploads=%d creates=%d want 2/1（主形象+1 参考图）", up, creates)
	}
}

// A19-2：10 并发同档案 sync → 仅 1 个真实 job。
func TestSyncConcurrentSingleJob(t *testing.T) {
	fake := &fakeUploader{uploadDelay: 20 * time.Millisecond}
	s, store, _, a := newSyncFixture(t, fake)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	jobs := make([]SyncJob, n)
	created := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, c, _, err := s.Start(ctx, a.ID)
			if err != nil {
				t.Error(err)
				return
			}
			jobs[i] = j
			created[i] = c
		}(i)
	}
	wg.Wait()

	createdN := 0
	for i := 0; i < n; i++ {
		if created[i] {
			createdN++
		}
		if jobs[i].ID != jobs[0].ID {
			t.Fatalf("并发返回不同 job：%v vs %v", jobs[i].ID, jobs[0].ID)
		}
	}
	if createdN != 1 {
		t.Fatalf("created 次数 = %d want 1", createdN)
	}
	waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateDone })
	if up, creates, _ := fake.counts(); up != 1 || creates != 1 {
		t.Fatalf("uploads=%d creates=%d want 1/1（10 并发仅 1 个真实 job）", up, creates)
	}
}

// A19-3：sha256 未变 → 200 复放 dedup:true；跨重启（新 SyncService 同一 store）仍复放。
func TestSyncDedupReplay(t *testing.T) {
	fake := &fakeUploader{}
	s, store, _, a := newSyncFixture(t, fake)
	ctx := context.Background()

	job1, created, _, err := s.Start(ctx, a.ID)
	if err != nil || !created {
		t.Fatalf("Start = %v %v", created, err)
	}
	waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateDone })

	job2, created, dedup, err := s.Start(ctx, a.ID)
	if err != nil || created || !dedup {
		t.Fatalf("复放 = created=%v dedup=%v err=%v", created, dedup, err)
	}
	if job2.ID != job1.ID || job2.State != SyncStateDone {
		t.Fatalf("复放 job = %+v", job2)
	}

	// 跨重启：进程内 job map 丢失，持久化终态仍保证 dedup 复放。
	s2 := NewSyncService(store, NewImageService(store, t.TempDir()), fake, nil)
	job3, created, dedup, err := s2.Start(ctx, a.ID)
	if err != nil || created || !dedup || job3.ID != job1.ID {
		t.Fatalf("跨重启复放 = created=%v dedup=%v job=%+v err=%v", created, dedup, job3, err)
	}
	if up, creates, _ := fake.counts(); up != 1 || creates != 1 {
		t.Fatalf("uploads=%d creates=%d want 1/1（dedup 不得重复上传/创建）", up, creates)
	}
}

// 补偿①：上传成功但 create 失败 → error(stage=creating)；重试从 create 续传，不重复上传。
func TestSyncResumeFromCreating(t *testing.T) {
	fake := &fakeUploader{createErrs: []error{errors.New("vidu create boom")}}
	s, store, _, a := newSyncFixture(t, fake)
	ctx := context.Background()

	if _, _, _, err := s.Start(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	st := waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateError })
	if st.Stage != SyncStageCreating {
		t.Fatalf("error stage = %q want creating", st.Stage)
	}

	job2, created, dedup, err := s.Start(ctx, a.ID)
	if err != nil || !created || dedup {
		t.Fatalf("重试 = created=%v dedup=%v err=%v", created, dedup, err)
	}
	if job2.ID == "" {
		t.Fatal("重试应派发新 job")
	}
	waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateDone })
	if up, creates, _ := fake.counts(); up != 1 || creates != 2 {
		t.Fatalf("uploads=%d creates=%d want 1/2（续传不重复上传）", up, creates)
	}
}

// 补偿②：create 成功但回填写库失败 → error(stage=persist)；重试只做写库。
func TestSyncResumePersistOnly(t *testing.T) {
	fake := &fakeUploader{}
	store := NewMemoryStore()
	images := NewImageService(store, t.TempDir())
	ctx := context.Background()
	asset, _, err := images.SaveUpload(ctx, "image", "a.png", bytes.NewReader(makePNG(t, 8, 8)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewService(store).Create(ctx, CreateInput{Name: "小雅", ImageAssetID: asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	// 包装 store：仅对 persist 终态写（vidu_sync_state=done）注入一次非 CAS 错误。
	flaky := &failPersistStore{MemoryAvatarStore: store}
	s := NewSyncService(flaky, images, fake, nil)

	if _, _, _, err := s.Start(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	st := waitSyncState(t, store, a.ID, func(v ViduSyncState) bool {
		return v.State == SyncStateError || v.State == SyncStateDone
	})
	if st.State != SyncStateError || st.Stage != SyncStagePersist {
		t.Fatalf("state = %+v want error/persist", st)
	}

	if _, created, _, err := s.Start(ctx, a.ID); err != nil || !created {
		t.Fatalf("重试 = created=%v err=%v", created, err)
	}
	waitSyncState(t, store, a.ID, func(v ViduSyncState) bool { return v.State == SyncStateDone })
	if up, creates, _ := fake.counts(); up != 1 || creates != 1 {
		t.Fatalf("uploads=%d creates=%d want 1/1（persist 重试只写库）", up, creates)
	}
	done, _, _ := store.Get(ctx, a.ID)
	if done.ViduAvatarID != "va-1" {
		t.Fatalf("重试后 vidu_avatar_id = %q", done.ViduAvatarID)
	}
}

// 开机对账（§2.2）：非终态 → error:interrupted；孤儿资产 delete_avatar best-effort。
func TestSyncReconcileOnBoot(t *testing.T) {
	fake := &fakeUploader{}
	store := NewMemoryStore()
	ctx := context.Background()
	a, err := NewService(store).Create(ctx, CreateInput{Name: "小雅"})
	if err != nil {
		t.Fatal(err)
	}
	// 伪造崩溃现场：creating 中途，create 已成功（ViduAvatarID 已落库）但 persist 未完成。
	cur, _, _ := store.Get(ctx, a.ID)
	cur.ViduSync = ViduSyncState{
		State: SyncStateCreating, Stage: SyncStageCreating, JobID: "job-x",
		ViduAvatarID: "va-orphan", AssetSHA256: "sha-x", UpdatedAtMs: time.Now().UnixMilli(),
	}
	if err := store.UpdateWithRevision(ctx, cur, cur.Revision); err != nil {
		t.Fatal(err)
	}

	NewSyncService(store, NewImageService(store, t.TempDir()), fake, nil).ReconcileOnBoot(ctx)

	_, _, deletes := fake.counts()
	if deletes != 1 {
		t.Fatalf("orphan delete_avatar 调用 = %d want 1", deletes)
	}
	got, _, _ := store.Get(ctx, a.ID)
	if got.ViduSync.State != SyncStateError || got.ViduSync.Stage != SyncStageInterrupted {
		t.Fatalf("对账后 state = %+v want error:interrupted", got.ViduSync)
	}
	if got.ViduSync.ViduAvatarID != "" {
		t.Fatalf("已清理孤儿 ID 应清空 = %+v", got.ViduSync)
	}
}

// failPersistStore persist 故障注入：仅第一次写入 done 终态（persist 阶段）失败。
type failPersistStore struct {
	*MemoryAvatarStore
	mu    sync.Mutex
	fired bool
}

func (f *failPersistStore) UpdateWithRevision(ctx context.Context, a Avatar, base int64) error {
	f.mu.Lock()
	if !f.fired && a.ViduSync.State == SyncStateDone {
		f.fired = true
		f.mu.Unlock()
		return errors.New("db write boom")
	}
	f.mu.Unlock()
	return f.MemoryAvatarStore.UpdateWithRevision(ctx, a, base)
}
