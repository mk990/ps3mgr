package ps2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestULRecordMatchesIso2OPLLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ul.cfg")
	fs := &Filesystem{}
	if err := fs.upsertULRecord(path, "Gran Turismo 4", "SCES_517.19", 3); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 64 {
		t.Fatalf("record length = %d", len(data))
	}
	if got := strings.TrimRight(string(data[:32]), "\x00"); got != "Gran Turismo 4" {
		t.Fatalf("name = %q", got)
	}
	if got := strings.TrimRight(string(data[32:47]), "\x00"); got != "ul.SCES_517.19" {
		t.Fatalf("image = %q", got)
	}
	if data[47] != 3 || data[48] != 0x14 || data[53] != 0x08 {
		t.Fatalf("record bytes are invalid: %x", data[47:54])
	}
}

func TestULRecordUpsertPreservesOtherGames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ul.cfg")
	fs := &Filesystem{}
	if err := fs.upsertULRecord(path, "A", "SLUS_000.01", 1); err != nil {
		t.Fatal(err)
	}
	if err := fs.upsertULRecord(path, "B", "SLES_000.02", 2); err != nil {
		t.Fatal(err)
	}
	if err := fs.upsertULRecord(path, "A", "SLUS_000.01", 3); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if len(data) != 128 {
		t.Fatalf("size = %d", len(data))
	}
	if data[64+47] != 3 {
		t.Fatalf("updated parts = %d", data[64+47])
	}
}

func TestDirectCopyPreparesLayoutSystemFilesAndVerifies(t *testing.T) {
	root := t.TempDir()
	system := t.TempDir()
	if err := os.WriteFile(filepath.Join(system, "OPNPS2LD.ELF"), []byte("opl"), 0644); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), "game.iso")
	payload := []byte("owned fixture")
	if err := os.WriteFile(iso, payload, 0644); err != nil {
		t.Fatal(err)
	}
	target := USBTarget{ID: "usb0", MountPath: root, Available: true}
	fs := &Filesystem{SystemDir: system}
	result, err := fs.InstallGame(context.Background(), Game{ID: "SCES-51719", Title: "Gran Turismo 4", ISOPath: iso, Size: int64(len(payload))}, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Strategy != "direct-copy" {
		t.Fatalf("strategy = %s", result.Strategy)
	}
	if _, err := os.Stat(filepath.Join(root, "DVD", "SCES_517.19.Gran Turismo 4.iso")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "OPNPS2LD.ELF")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Verify(context.Background(), result); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRequiresSystemFiles(t *testing.T) {
	fs := &Filesystem{SystemDir: filepath.Join(t.TempDir(), "missing")}
	err := fs.Prepare(context.Background(), USBTarget{MountPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "PS2 system directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestOPLCRCIsStable(t *testing.T) {
	if got := oplCRC("Gran Turismo 4"); got != 0xC45A5E1D {
		t.Fatalf("CRC = %08X", got)
	}
}

func TestUSBExtremeKnownSplitFixtureAndProgress(t *testing.T) {
	targetRoot, systemRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(systemRoot, "OPL.ELF"), []byte("system"), 0644); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), "game.iso")
	payload := []byte("abcdefghij")
	if err := os.WriteFile(iso, payload, 0644); err != nil {
		t.Fatal(err)
	}
	fs := &Filesystem{SystemDir: systemRoot, DirectCopyLimit: 4, PartSize: 4}
	seen := map[State]bool{}
	result, err := fs.InstallGame(context.Background(), Game{ID: "SCES-51719", Title: "Gran Turismo 4", ISOPath: iso, Size: int64(len(payload))}, USBTarget{ID: "usb0", MountPath: targetRoot, Available: true}, func(progress Progress) { seen[progress.Stage] = true })
	if err != nil {
		t.Fatal(err)
	}
	if result.Strategy != "usb-extreme" || len(result.Files) != 3 {
		t.Fatalf("result = %#v", result)
	}
	prefix := "ul.C45A5E1D.SCES_517.19"
	for index, want := range []string{"abcd", "efgh", "ij"} {
		path := filepath.Join(targetRoot, prefix+fmt.Sprintf(".%02d", index))
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != want {
			t.Fatalf("part %d = %q", index, data)
		}
	}
	if !seen[StateConverting] || !seen[StateWriting] {
		t.Fatalf("progress stages = %#v", seen)
	}
	if err := fs.Verify(context.Background(), result); err != nil {
		t.Fatal(err)
	}
}
