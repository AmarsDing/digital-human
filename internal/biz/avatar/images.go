package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg" // DecodeConfig 需要注册格式
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// 图片上传约束（v3 §5.3/§9.1）：png/jpeg ≤10MB、宽高 ≤4096；webp/svg 拒绝。
const (
	MaxImageBytes  = 10 << 20
	MaxImagePixels = 4096
)

// 图片管线哨兵错误：server 层映射 400。
var (
	ErrImageTooLarge = errors.New("image_too_large")
	ErrImageType     = errors.New("unsupported_image_type")
	ErrImagePixels   = errors.New("image_pixels_exceeded")
)

// ImageService 图片上传/读取管线（M2：限长/嗅探/像素/dedup/落盘；
// 并发 dedup 强化与 90 天续期属 M3 素材管线）。
type ImageService struct {
	store ImageStore
	dir   string // 内容寻址存储根（storage/images）
}

func NewImageService(store ImageStore, dir string) *ImageService {
	return &ImageService{store: store, dir: dir}
}

// 白名单：http.DetectContentType 嗅探结果 → 落盘扩展名（§5.3）。
var imageExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
}

// SaveUpload 顺序固定（§5.3）：限长 → 嗅探白名单 → 像素 → sha256 dedup → 落盘 → 登记。
// dedup=true 时复用首个同 sha256 素材记录，不落盘不新建。
// 并发上传安全（M3）：同 sha256 并发时双方均写同一内容寻址路径（内容相同，幂等），
// PutAsset 撞唯一索引幂等复用；登记后回查返回 canonical 首记录，杜绝并发双方各拿
// 各自 asset id 导致后续 AddRef 绑定到未入库行的竞态。
func (s *ImageService) SaveUpload(ctx context.Context, kind, filename string, r io.Reader) (Asset, bool, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxImageBytes+1))
	if err != nil {
		return Asset{}, false, err
	}
	if len(buf) > MaxImageBytes {
		return Asset{}, false, ErrImageTooLarge
	}
	ext, ok := imageExts[http.DetectContentType(buf)]
	if !ok {
		return Asset{}, false, ErrImageType // webp/svg/gif 等拒绝
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return Asset{}, false, ErrImageType
	}
	if cfg.Width > MaxImagePixels || cfg.Height > MaxImagePixels {
		return Asset{}, false, ErrImagePixels
	}
	sum := sha256.Sum256(buf)
	sha := hex.EncodeToString(sum[:])
	if hit, ok, err := s.store.FindAssetBySHA256(ctx, sha); err != nil {
		return Asset{}, false, err
	} else if ok {
		return hit, true, nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Asset{}, false, err
	}
	rel := filepath.Join(s.dir, sha[:16]+ext)
	if err := os.WriteFile(rel, buf, 0o644); err != nil {
		return Asset{}, false, err
	}
	if filename == "" {
		filename = sha[:16] + ext
	}
	a := Asset{ID: uuid.NewString(), SHA256: sha, Kind: kind, Name: filename, Path: rel}
	if err := s.store.PutAsset(ctx, a); err != nil {
		return Asset{}, false, err
	}
	// 并发同 sha256：PutAsset 幂等复用首次记录，回查返回 canonical 行（见函数注释）。
	if hit, ok, err := s.store.FindAssetBySHA256(ctx, sha); err != nil {
		return Asset{}, false, err
	} else if ok {
		return hit, hit.ID != a.ID, nil
	}
	return a, false, nil
}

// ReadAsset 读取素材字节流并嗅探 Content-Type（M3 sync-vidu 上传 Vidu 用）；
// 路径解析复用 ResolvePath 的目录穿越防护。
func (s *ImageService) ReadAsset(ctx context.Context, assetID string) ([]byte, string, error) {
	p, err := s.ResolvePath(ctx, assetID)
	if err != nil {
		return nil, "", err
	}
	buf, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	return buf, http.DetectContentType(buf), nil
}

// ResolvePath 解析素材落盘绝对路径（图片路由用）：路径只来自 DB，
// 并强制限定在存储根内（防目录穿越，对齐 serveKouboAudio 先例）。
func (s *ImageService) ResolvePath(ctx context.Context, assetID string) (string, error) {
	a, ok, err := s.store.GetAsset(ctx, assetID)
	if err != nil {
		return "", err
	}
	if !ok || a.Path == "" {
		return "", ErrNotFound
	}
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return "", err
	}
	p, err := filepath.Abs(a.Path)
	if err != nil {
		return "", err
	}
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", ErrNotFound
	}
	return p, nil
}
