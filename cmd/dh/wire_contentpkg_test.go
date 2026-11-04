package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/conf"
)

func TestProvideContentpkgStorageCapability(t *testing.T) {
	t.Run("unconfigured remains memory fake", func(t *testing.T) {
		t.Setenv("DH_CONTENTPKG_DIR", "")
		rt := &panelRuntime{}
		svc := provideContentpkg(contentpkg.NewMemoryStore(), rt)
		if rt.deps.Contentpkg != svc {
			t.Fatal("content service not attached")
		}
		mx := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)(context.Background())
		if got := mx.Get("content_storage"); got != capability.Fake {
			t.Fatalf("content_storage=%s want=fake", got)
		}
	})

	t.Run("disk probe is real then unknown after failure", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "contentpkg")
		t.Setenv("DH_CONTENTPKG_DIR", root)
		rt := &panelRuntime{}
		provideContentpkg(contentpkg.NewMemoryStore(), rt)
		caps := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)
		if got := caps(context.Background()).Get("content_storage"); got != capability.Real {
			t.Fatalf("content_storage=%s want=real after real disk probe", got)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := caps(context.Background()).Get("content_storage"); got != capability.Unknown {
			t.Fatalf("content_storage=%s want=unknown after failed fresh probe", got)
		}
	})
}
