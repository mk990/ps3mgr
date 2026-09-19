package ps4

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
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

func TestNameFormatMatchesWholeWordsOnly(t *testing.T) {
	for name, want := range map[string]string{
		"CUSA12345-base":      "pkg-game",
		"Game [BASE] v1.00":   "pkg-game",
		"base_Game":           "pkg-game",
		"Game.Base.Part01":    "pkg-game",
		"Game-basegame":       "pkg-game",
		"Game-patch":          "pkg-patch",
		"Game_update02":       "pkg-patch",
		"Game-dlc01":          "pkg-dlc",
		"Game addon":          "pkg-dlc",
		"Game-base-patch01":   "pkg-game",
		"Baseball 2024":       "",
		"Database Tool":       "",
		"Metal Gear Basecamp": "",
		"Updated Edition":     "",
		"Minecraft_CUSA00265_v3.43_BACKPORT_[505-672-7xx-900-1100-1200]_OPOISSO893-P30Day.ir": "",
	} {
		if got := nameFormat(name); got != want {
			t.Errorf("nameFormat(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestLibraryFileNameOverridesPackageHeaderType(t *testing.T) {
	root := t.TempDir()
	contentID := "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"
	// Every header below says "patch"; only the names disagree.
	writePKGFixture(t, filepath.Join(root, "Game-base.pkg"), contentID, 0x1e, 0x100)
	writePKGFixture(t, filepath.Join(root, "Game-dlc01.pkg"), contentID, 0x1e, 0x100)
	writePKGFixture(t, filepath.Join(root, "Game-unlabelled.pkg"), contentID, 0x1e, 0x100)
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	formats := make(map[string]Package, len(items))
	for _, item := range items {
		formats[item.Parts[0].Name] = item
	}
	for name, want := range map[string]string{
		"Game-base.pkg":       "pkg-game",
		"Game-dlc01.pkg":      "pkg-dlc",
		"Game-unlabelled.pkg": "pkg-patch",
	} {
		if got := formats[name].Format; got != want {
			t.Errorf("%s format = %q, want %q", name, got, want)
		}
	}
	if !formats["Game-base.pkg"].NamedBase {
		t.Error("a base named by its file name should be marked as such")
	}
	if formats["Game-unlabelled.pkg"].NamedBase {
		t.Error("a header-classified package must not be marked as named")
	}
}

func TestLibraryKeepsLowestVersionAsTitleBase(t *testing.T) {
	root := t.TempDir()
	contentID := "EP4433-CUSA00265_00-MINECRAFTPS40000"
	// Both headers say "game": the merged backport is built as a full package,
	// so only the application version separates it from the real base.
	writePKGFixture(t, filepath.Join(root, "Minecraft_CUSA00265_v3.43_BACKPORT_[505-672].pkg"), contentID, 0x1a, 0x100)
	writePKGFixture(t, filepath.Join(root, "Minecraft.PlayStation4.Edition_CUSA00265_v1.00_[1.70].pkg"), contentID, 0x1a, 0x100)
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two packages, got %#v", items)
	}
	for _, item := range items {
		wantFormat, wantVersion := "pkg-game", "01.00"
		if strings.Contains(item.Parts[0].Name, "BACKPORT") {
			wantFormat, wantVersion = "pkg-patch", "03.43"
		}
		if item.Format != wantFormat || item.Version != wantVersion {
			t.Errorf("%s = %s/%s, want %s/%s", item.Parts[0].Name, item.Format, item.Version, wantFormat, wantVersion)
		}
	}
}

func TestLibraryKeepsASoleBackportInstallableAsBase(t *testing.T) {
	root := t.TempDir()
	contentID := "EP4433-CUSA00265_00-MINECRAFTPS40000"
	writePKGFixture(t, filepath.Join(root, "Minecraft_CUSA00265_v3.43_BACKPORT.pkg"), contentID, 0x1a, 0x100)
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Format != "pkg-game" {
		t.Fatalf("a title's only game package must stay its base: %#v", items)
	}
}

func TestLibraryNamedBaseOutranksALowerVersion(t *testing.T) {
	root := t.TempDir()
	contentID := "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"
	writePKGFixture(t, filepath.Join(root, "Game_v1.00_repack.pkg"), contentID, 0x1a, 0x100)
	writePKGFixture(t, filepath.Join(root, "Game_v2.05_base.pkg"), contentID, 0x1a, 0x100)
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		want := "pkg-patch"
		if strings.Contains(item.Parts[0].Name, "base") {
			want = "pkg-game"
		}
		if item.Format != want {
			t.Errorf("%s format = %q, want %q", item.Parts[0].Name, item.Format, want)
		}
	}
}

func TestLibraryReadsBothVersionSpellings(t *testing.T) {
	root := t.TempDir()
	for name, want := range map[string]string{
		"UP0001-CUSA00001_00-AAAAAAAAAAAAAAAA-A0102-V0100.pkg":    "01.00",
		"UP0001-CUSA00002_00-BBBBBBBBBBBBBBBB_v1.00_[1.70].pkg":   "01.00",
		"UP0001-CUSA00003_00-CCCCCCCCCCCCCCCC_v3.43_BACKPORT.pkg": "03.43",
		"UP0001-CUSA00004_00-DDDDDDDDDDDDDDDD_v10.02.pkg":         "10.02",
		"UP0001-CUSA00005_00-EEEEEEEEEEEEEEEE.pkg":                "",
	} {
		contentID := name[:36]
		writePKGFixture(t, filepath.Join(root, name), contentID, 0x1e, 0x100)
		t.Logf("%s wants version %q", name, want)
	}
	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	versions := make(map[string]string, len(items))
	for _, item := range items {
		versions[item.TitleID] = item.Version
	}
	for titleID, want := range map[string]string{"CUSA00001": "01.00", "CUSA00002": "01.00", "CUSA00003": "03.43", "CUSA00004": "10.02", "CUSA00005": ""} {
		if got := versions[titleID]; got != want {
			t.Errorf("%s version = %q, want %q", titleID, got, want)
		}
	}
}
