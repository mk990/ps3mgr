package ps4

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestCoverCacheExtractsEmbeddedIconOnceAndReusesIt(t *testing.T) {
	root := t.TempDir()
	pkgPath := filepath.Join(root, "CUSA12345.pkg")
	writePKGIconFixture(t, pkgPath)
	cache := &CoverCache{}
	packages := []Package{{ID: "package-id", TitleID: "CUSA12345", Parts: []PackagePart{{Path: pkgPath, Name: filepath.Base(pkgPath)}}}}

	extracted, failures := cache.Populate(context.Background(), root, packages)
	if extracted != 1 || len(failures) != 0 {
		t.Fatalf("extracted=%d failures=%#v", extracted, failures)
	}
	want := filepath.Join(root, "covers", "CUSA12345.png")
	if packages[0].CoverPath != want || packages[0].CoverURL != "/api/ps4/games/package-id/cover" {
		t.Fatalf("unexpected package cover: %#v", packages[0])
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}

	// A later scan must use the cache without opening or parsing the PKG again.
	if err := os.Remove(pkgPath); err != nil {
		t.Fatal(err)
	}
	fresh := []Package{{ID: "package-id", TitleID: "CUSA12345", Parts: []PackagePart{{Path: pkgPath}}}}
	extracted, failures = cache.Populate(context.Background(), root, fresh)
	if extracted != 0 || len(failures) != 0 || fresh[0].CoverPath != want {
		t.Fatalf("cache was not reused: extracted=%d failures=%#v package=%#v", extracted, failures, fresh[0])
	}
	status := cache.Status(root)
	if !status.Available || !status.Writable || status.Images != 1 || status.CacheDir != filepath.Join(root, "covers") {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestCoverCachePrefersManualTitleIDImage(t *testing.T) {
	root := t.TempDir()
	manual := filepath.Join(root, "covers", "CUSA54321.jpg")
	if err := os.MkdirAll(filepath.Dir(manual), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manual, []byte("manual image fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	packages := []Package{{ID: "manual-package", TitleID: "CUSA54321", Parts: []PackagePart{{Path: filepath.Join(root, "missing.pkg")}}}}
	extracted, failures := (&CoverCache{}).Populate(context.Background(), root, packages)
	if extracted != 0 || len(failures) != 0 || packages[0].CoverPath != manual {
		t.Fatalf("manual cover not preferred: extracted=%d failures=%#v package=%#v", extracted, failures, packages[0])
	}
}

func TestCoverCacheDoesNotCreateMissingPS4Library(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	status := (&CoverCache{}).Status(root)
	if status.Available || status.Error == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("missing PS4 library was created, stat error: %v", err)
	}
}

func writePKGIconFixture(t *testing.T, path string) {
	t.Helper()
	icon := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	data := make([]byte, 0x200+len(icon))
	binary.BigEndian.PutUint32(data[:4], 0x7f434e54)
	binary.BigEndian.PutUint32(data[16:20], 1)
	binary.BigEndian.PutUint32(data[24:28], 0x100)
	binary.BigEndian.PutUint32(data[0x100:0x104], icon0EntryID)
	binary.BigEndian.PutUint32(data[0x110:0x114], 0x200)
	binary.BigEndian.PutUint32(data[0x114:0x118], uint32(len(icon)))
	copy(data[0x200:], icon)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
