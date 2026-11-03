package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"normal.wav":                   "normal.wav",
		"a/b/c.mp3":                    "c.mp3",
		"voice.wav\x00":                "voice.wav",
		"=?utf-8?B?5L2g5aW9Lndhdg==?=": "你好.wav",
		"clip":                         "clip.wav",
		"report 2024 (final).WAV":      "report 2024 (final).WAV",
	}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Fatalf("CleanName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestImportFolderSkipsDuplicates(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.wav"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := NewLibrary(dst)
	res, err := lib.ImportFolder(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || res.Skipped != 0 {
		t.Fatalf("res=%+v", res)
	}
	res, err = lib.ImportFolder(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || res.Skipped != 1 {
		t.Fatalf("second import=%+v", res)
	}
}

func TestUploadAndPathGuard(t *testing.T) {
	dir := t.TempDir()
	lib := NewLibrary(dir)
	f, err := lib.SaveUpload("x.wav", bytes.NewReader([]byte("abc")))
	if err != nil || f.Size != 3 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	if _, _, err := lib.Path("../escape.wav"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if err := lib.Delete("x.wav"); err != nil {
		t.Fatal(err)
	}
}

func TestFakeDecoderReturnsPCM(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := FakeDecoder{MS: 200}
	pcm, err := d.Decode(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 9600 {
		t.Fatalf("len=%d", len(pcm))
	}
}
