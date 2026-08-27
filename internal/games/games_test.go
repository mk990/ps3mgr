package games

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"ps3mgr/internal/domain"
)

func TestParseSFOAndLibraryScan(t *testing.T) {
	root := t.TempDir()
	gameDir := filepath.Join(root, "Folder Name", "PS3_GAME")
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sfo := makeSFO(t, [][2]string{{"TITLE", "Actual Game"}, {"TITLE_ID", "BLES01717"}, {"APP_VER", "01.02"}})
	if err := os.WriteFile(filepath.Join(gameDir, "PARAM.SFO"), sfo, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "ICON0.PNG"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "DATA.BIN"), make([]byte, 19), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := NewLibrary().Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d games", len(items))
	}
	game := items[0]
	if game.Title != "Actual Game" || game.ID != "BLES01717" || game.Version != "01.02" || game.Region != "Europe" {
		t.Fatalf("unexpected metadata: %+v", game)
	}
	if game.Size != int64(len(sfo)+3+19) || game.IconPath == "" {
		t.Fatalf("unexpected files: %+v", game)
	}
}

func TestLibraryFallbackAndInvalidDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "No Metadata"), 0o755); err != nil {
		t.Fatal(err)
	}
	items, err := NewLibrary().Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "No Metadata" || items[0].ID != "" {
		t.Fatalf("fallback failed: %+v", items)
	}
	if _, err := NewLibrary().Scan(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected inaccessible directory error")
	}
}

func TestParseSFORejectsMalformedData(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not an sfo"), makeSFO(t, [][2]string{{"TITLE", "x"}})[:25]} {
		if _, err := ParseSFO(data); err == nil {
			t.Fatalf("expected error for %d bytes", len(data))
		}
	}
}

func TestComparePrefersIDAndFallsBackToNormalizedTitle(t *testing.T) {
	local := []domain.Game{{ID: "BLES1", Title: "Different name"}, {Title: "Dead Space 3"}, {Title: "Missing"}}
	remote := []domain.Game{{ID: "bles1", Title: "Anything"}, {Title: "Dead-Space_3"}}
	result := Compare(local, remote)
	if !result[0].Installed || !result[1].Installed || result[2].Installed {
		t.Fatalf("comparison failed: %+v", result)
	}
	if result[2].State != domain.StateNotInstalled {
		t.Fatalf("unexpected missing state: %s", result[2].State)
	}
}

func makeSFO(t *testing.T, values [][2]string) []byte {
	t.Helper()
	var keys, data bytes.Buffer
	type entry struct{ keyOffset, length, dataOffset int }
	entries := make([]entry, 0, len(values))
	for _, pair := range values {
		value := append([]byte(pair[1]), 0)
		entries = append(entries, entry{keys.Len(), len(value), data.Len()})
		keys.WriteString(pair[0])
		keys.WriteByte(0)
		data.Write(value)
	}
	keyOffset := 20 + len(entries)*16
	dataOffset := keyOffset + keys.Len()
	result := make([]byte, dataOffset+data.Len())
	binary.LittleEndian.PutUint32(result[0:4], sfoMagic)
	binary.LittleEndian.PutUint32(result[4:8], 0x00000101)
	binary.LittleEndian.PutUint32(result[8:12], uint32(keyOffset))
	binary.LittleEndian.PutUint32(result[12:16], uint32(dataOffset))
	binary.LittleEndian.PutUint32(result[16:20], uint32(len(entries)))
	for i, item := range entries {
		position := 20 + i*16
		binary.LittleEndian.PutUint16(result[position:position+2], uint16(item.keyOffset))
		binary.LittleEndian.PutUint16(result[position+2:position+4], 0x0204)
		binary.LittleEndian.PutUint32(result[position+4:position+8], uint32(item.length))
		binary.LittleEndian.PutUint32(result[position+8:position+12], uint32(item.length))
		binary.LittleEndian.PutUint32(result[position+12:position+16], uint32(item.dataOffset))
	}
	copy(result[keyOffset:], keys.Bytes())
	copy(result[dataOffset:], data.Bytes())
	return result
}
