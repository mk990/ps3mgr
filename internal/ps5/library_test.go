package ps5

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ps3mgr/internal/domain"
)

func TestLibraryDiscoversShadowMountPlusFormatsAndFolderMetadata(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Astro PPSA01325.ffpfsc", "Demon CUSA12345.exfat", "Other.FFPFS", "Package.ffpkg", "ignore.iso"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	folder := filepath.Join(root, "Folder Game")
	if err := os.MkdirAll(filepath.Join(folder, "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"titleId":"PPSA99999","localizedParameters":{"en-US":{"titleName":"Folder Title"}}}`
	if err := os.WriteFile(filepath.Join(folder, "sce_sys", "param.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "sce_sys", "icon0.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backports", "Ignored", "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backports", "Ignored", "sce_sys", "param.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d games, want 5: %+v", len(items), items)
	}
	byFormat := make(map[string]domain.Game)
	for _, game := range items {
		byFormat[game.Format] = game
	}
	for _, format := range []string{"ffpfsc", "exfat", "ffpfs", "ffpkg", "folder"} {
		if _, ok := byFormat[format]; !ok {
			t.Errorf("format %q was not discovered", format)
		}
	}
	if game := byFormat["folder"]; game.ID != "PPSA99999" || game.Title != "Folder Title" || game.IconURL == "" {
		t.Fatalf("folder metadata = %+v", game)
	}
}

func TestServiceUsesPS5PortDestinationAndQueueNamespace(t *testing.T) {
	service := NewService(t.TempDir(), "", "anonymous", "", 0, 1, time.Millisecond, time.Second, nil)
	defer service.Close(context.Background())
	if service.Scanner.Port != "2121" || service.FTP.Port != 2121 {
		t.Fatalf("PS5 FTP ports: scanner=%s service=%d", service.Scanner.Port, service.FTP.Port)
	}
	if service.RemoteDir != "/data/etaHEN/games" {
		t.Fatalf("remote destination = %q", service.RemoteDir)
	}
	if _, err := service.LocalGames(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enqueue("127.0.0.1", []string{"missing"}, false); err == nil {
		t.Fatal("unknown game should not be queued")
	}
}
