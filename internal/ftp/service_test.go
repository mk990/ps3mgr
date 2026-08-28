package ftp

import (
	"testing"

	"ps3mgr/internal/domain"
)

func TestLooksLikePS3RequiresFilesystemMarkers(t *testing.T) {
	for _, roots := range [][]string{{"dev_hdd0"}, {"DEV_FLASH", "other"}} {
		if !looksLikePS3(roots) {
			t.Fatalf("expected PS3 markers in %v", roots)
		}
	}
	if looksLikePS3([]string{"pub", "uploads", "games"}) {
		t.Fatal("a generic FTP server must not be detected as a PS3")
	}
}

func TestCleanPathRemovesCommandInjection(t *testing.T) {
	if got := cleanPath("/dev_hdd0/GAMES\r\nDELE /file"); got != "/dev_hdd0/GAMESDELE /file" {
		t.Fatalf("unexpected clean path %q", got)
	}
}

func TestDownloadNameUsesTitleIDAndSanitizesTitle(t *testing.T) {
	game := domain.Game{ID: "CUSA12345", Title: "My/Game: Deluxe", RemotePath: "/data/games/CUSA12345"}
	if got := DownloadName(game); got != "CUSA12345 - My-Game- Deluxe" {
		t.Fatalf("download name = %q", got)
	}
}
