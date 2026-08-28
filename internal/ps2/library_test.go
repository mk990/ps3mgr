package ps2

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibraryDiscoversISOAndReadsSystemCNF(t *testing.T) {
	root := t.TempDir()
	known := filepath.Join(root, "Gran Turismo 4.ISO")
	writeTestISO(t, known, "BOOT2 = cdrom0:\\SCES_517.19;1\r\n")
	if err := os.WriteFile(filepath.Join(root, "Mystery.iso"), []byte("not an iso"), 0644); err != nil {
		t.Fatal(err)
	}
	games, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("got %d games", len(games))
	}
	var found bool
	for _, game := range games {
		if game.ISOFilename == "Gran Turismo 4.ISO" {
			found = true
			if game.ID != "SCES-51719" {
				t.Fatalf("ID = %q", game.ID)
			}
		}
	}
	if !found {
		t.Fatal("uppercase ISO not found")
	}
}

func TestFilenameIDFallbackAndUnknownAllowed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SLUS_203.12 Game.iso"), []byte("bad"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Homebrew.iso"), []byte("bad"), 0644); err != nil {
		t.Fatal(err)
	}
	games, err := (Library{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, game := range games {
		ids[game.ID] = true
	}
	if !ids["SLUS-20312"] || !ids["unknown"] {
		t.Fatalf("IDs = %#v", ids)
	}
}

func TestLibraryUsesManualSerialNamedCovers(t *testing.T) {
	for _, test := range []struct {
		name      string
		directory string
		filename  string
	}{
		{name: "covers root", directory: "covers", filename: "SCES-51719.JPG"},
		{name: "repository default", directory: filepath.Join("covers", "default"), filename: "SCES-51719.jpg"},
		{name: "repository 3d", directory: filepath.Join("covers", "3d"), filename: "SCES-51719.png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			iso := filepath.Join(root, "Gran Turismo 4 SCES_517.19.iso")
			if err := os.WriteFile(iso, []byte("fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			coverDirectory := filepath.Join(root, test.directory)
			if err := os.MkdirAll(coverDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			cover := filepath.Join(coverDirectory, test.filename)
			if err := os.WriteFile(cover, []byte("local cover fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Even an ISO-named file under covers must not become a game.
			if err := os.WriteFile(filepath.Join(coverDirectory, "ignore.iso"), []byte("fixture"), 0o644); err != nil {
				t.Fatal(err)
			}

			games, err := (Library{}).Scan(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if len(games) != 1 {
				t.Fatalf("got %d games, want 1", len(games))
			}
			if games[0].ID != "SCES-51719" || games[0].CoverPath != cover {
				t.Fatalf("game = %#v, want local cover %q", games[0], cover)
			}
			if games[0].CoverURL == "" || strings.HasPrefix(games[0].CoverURL, "http") {
				t.Fatalf("cover URL = %q, want local API URL", games[0].CoverURL)
			}
		})
	}
}

func writeTestISO(t *testing.T, path, cnf string) {
	t.Helper()
	image := make([]byte, 24*2048)
	pvd := image[16*2048 : 17*2048]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	writeRecord(pvd[156:], 20, 2048, []byte{0})
	root := image[20*2048 : 21*2048]
	n := writeRecord(root, 21, uint32(len(cnf)), []byte("SYSTEM.CNF;1"))
	_ = n
	copy(image[21*2048:], cnf)
	if err := os.WriteFile(path, image, 0644); err != nil {
		t.Fatal(err)
	}
}
func writeRecord(dst []byte, extent, size uint32, name []byte) int {
	length := 33 + len(name)
	if length%2 != 0 {
		length++
	}
	dst[0] = byte(length)
	binary.LittleEndian.PutUint32(dst[2:6], extent)
	binary.BigEndian.PutUint32(dst[6:10], extent)
	binary.LittleEndian.PutUint32(dst[10:14], size)
	binary.BigEndian.PutUint32(dst[14:18], size)
	dst[25] = 0
	binary.LittleEndian.PutUint16(dst[28:30], 1)
	binary.BigEndian.PutUint16(dst[30:32], 1)
	dst[32] = byte(len(name))
	copy(dst[33:], name)
	return length
}
