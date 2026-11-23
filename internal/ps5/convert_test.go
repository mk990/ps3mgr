package ps5

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func conversionFolder(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "Game v1.2")
	if err := os.MkdirAll(filepath.Join(source, "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"sce_sys/param.json": `{"titleId":"PPSA00001","titleName":"Game"}`, "eboot.bin": "sample payload"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, source
}

func TestConvertFolderProtectsSourceAndExistingOutput(t *testing.T) {
	root, source := conversionFolder(t)
	service := Service{GameDir: root}
	ctx := context.Background()
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ game, format, message string }{
		{"Game", "../outside", "choose exfat"},
		{"../outside", "exfat", "not found"},
		{"Game", "exfat", "requires MkPFS"},
	} {
		if _, err := service.ConvertFolder(ctx, tc.game, tc.format); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("convert(%q, %q) = %v", tc.game, tc.format, err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatalf("failed conversions left files behind: %v, %v", entries, err)
	}
	output := source + ".ffpkg"
	if err := os.WriteFile(output, []byte("original image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConvertFolder(ctx, "PPSA00001", "FFPKG"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output: %v", err)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "original image" {
		t.Fatalf("existing image changed: %q, %v", data, err)
	}
}

func TestConvertFolderWithInstalledBuilders(t *testing.T) {
	for format, builder := range map[string]string{"exfat": "mkpfs", "ffpkg": "makefs", "ffpfsc": "mkpfs"} {
		t.Run(format, func(t *testing.T) {
			if _, err := exec.LookPath(builder); err != nil {
				t.Skipf("%s not installed", builder)
			}
			root, source := conversionFolder(t)
			service := Service{GameDir: root}
			ctx := context.Background()
			name, err := service.ConvertFolder(ctx, "Game", format)
			if err != nil || name != filepath.Base(source)+"."+format {
				t.Fatalf("convert = %q, %v", name, err)
			}
			items, err := service.LocalGames(ctx, "")
			if err != nil || len(items) != 2 || items[0].LibraryID == items[1].LibraryID || items[0].ID != items[1].ID {
				t.Fatalf("source and converted edition = %+v, %v", items, err)
			}
			if data, err := os.ReadFile(filepath.Join(source, "eboot.bin")); err != nil || string(data) != "sample payload" {
				t.Fatalf("source changed: %q, %v", data, err)
			}
		})
	}
}
