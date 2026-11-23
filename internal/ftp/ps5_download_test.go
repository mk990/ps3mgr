package ftp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"ps3mgr/internal/ftp"
	"ps3mgr/internal/ps5"
)

func TestPS5FilePullKeepsFilenameAndAvatar(t *testing.T) {
	icon := []byte("\x89PNG\r\n\x1a\n")
	files := map[string][]byte{
		"/games/Plain Game.ffpkg":                                       []byte("mounted image"),
		"/mnt/shadowmnt/Plain Game_bb6d0ab8/sce_sys/icon0.png":          icon,
		"/games/Game v1.2.PKG":                                          []byte("package"),
		"/games/Game v1.2.PNG":                                          icon,
		"/games/Another PPSA12345.FFPKG":                                []byte("image"),
		"/user/appmeta/PPSA12345/icon0.png":                             icon,
		"/games/Compressed Game.FFPFSC":                                 []byte("container holding exFAT"),
		"/mnt/shadowmnt/pfsc/Compressed Game_c2dc7cab/Inner Game.ExFAT": []byte("inner image"),
		"/mnt/shadowmnt/Inner Game_28a41059/sce_sys/icon0.png":          icon,
		"/games/Compressed PFS.ffpfsc":                                  []byte("container holding PFS"),
		"/mnt/shadowmnt/pfsc/Compressed PFS_e319cfd2/pfs_image.dat":     []byte("inner image"),
		"/mnt/shadowmnt/pfs_image_5a423cfe/sce_sys/icon0.png":           icon,
		"/games/Unmounted.ffpfsc":                                       []byte("unmounted image"),
	}
	server := ftp.NewFileFTPServer(t, files)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatal(err)
	}
	client := ps5.NewFTP("anonymous", "", time.Second, port, "/games")
	ctx := context.Background()
	items, err := client.RemoteGames(ctx, "127.0.0.1")
	if err != nil || len(items) != 6 {
		t.Fatalf("remote games = %+v, %v", items, err)
	}
	root := t.TempDir()
	for _, game := range items {
		if err := client.DownloadGame(ctx, "127.0.0.1", game, root, nil); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.Base(game.RemotePath)))
		if err != nil || !bytes.Equal(data, files[game.RemotePath]) {
			t.Fatalf("pulled %q = %q, %v", game.RemotePath, data, err)
		}
	}
	local, err := (ps5.Library{}).Scan(ctx, root)
	if err != nil || len(local) != 6 {
		t.Fatalf("local games = %+v, %v", local, err)
	}
	for _, game := range local {
		wantAvatar := filepath.Base(game.LocalPath) != "Unmounted.ffpfsc"
		if (game.IconURL != "") != wantAvatar {
			t.Errorf("avatar for %s = %q, want present = %v", game.LocalPath, game.IconURL, wantAvatar)
		}
	}
}
