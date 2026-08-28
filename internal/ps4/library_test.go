package ps4

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryDiscoversAndOrdersMultipartPackages(t *testing.T) {
	root := t.TempDir()
	contentID := "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"
	writePKGFixture(t, filepath.Join(root, contentID+"-A0102-V0100_1.pkg"), contentID, 0x1e, 513)
	writePKGFixture(t, filepath.Join(root, contentID+"-A0102-V0100_0.PKG"), contentID, 0x1e, 512)
	if err := os.WriteFile(filepath.Join(root, "not-a-package.pkg"), []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one grouped package, got %#v", items)
	}
	pkg := items[0]
	if pkg.TitleID != "CUSA12345" || pkg.ContentID != contentID || pkg.Format != "pkg-patch" || pkg.Version != "01.00" {
		t.Fatalf("unexpected metadata: %+v", pkg)
	}
	if len(pkg.Parts) != 2 || pkg.Parts[0].Name[len(pkg.Parts[0].Name)-5:] != "0.PKG" || pkg.Size != 1025 {
		t.Fatalf("multipart ordering/size incorrect: %+v", pkg.Parts)
	}
}

func TestLibraryRejectsMissingRoot(t *testing.T) {
	if _, err := (Library{}).Scan(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing directory error")
	}
}

func TestLibraryDoesNotMergeDifferentPatchVersions(t *testing.T) {
	root := t.TempDir()
	contentID := "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"
	writePKGFixture(t, filepath.Join(root, contentID+"-A0101-V0100.pkg"), contentID, 0x1e, 0x100)
	writePKGFixture(t, filepath.Join(root, contentID+"-A0102-V0100.pkg"), contentID, 0x1e, 0x100)
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("different patch versions were merged: %+v", items)
	}
}

func writePKGFixture(t *testing.T, path, contentID string, contentType uint32, size int) {
	t.Helper()
	if size < 0x100 {
		size = 0x100
	}
	data := make([]byte, size)
	binary.BigEndian.PutUint32(data[:4], 0x7f434e54)
	copy(data[0x40:0x70], contentID)
	binary.BigEndian.PutUint32(data[0x74:0x78], contentType)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
