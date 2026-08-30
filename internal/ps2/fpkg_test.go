package ps2

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ps3mgr/internal/orbis"
)

// emulatorTemplate returns the bundled PS2 emulator PKG, skipping when absent.
func emulatorTemplate(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "PS22PS4-GUI", "bin", "emulators", "JakV2.pkg")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("emulator template is not available: %v", err)
	}
	return path
}

func writeCover(t *testing.T, path string) {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			picture.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 2), B: 0x40, A: 0xff})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
}

func TestFPKGBuilderConvertsWithEmulatorTemplate(t *testing.T) {
	root := t.TempDir()
	output, support := filepath.Join(root, "ps4"), filepath.Join(root, "system")
	for _, directory := range []string{output, support} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	iso := filepath.Join(root, "Game.iso")
	isoData := bytes.Repeat([]byte("owned PS2 disc image fixture\n"), 4096)
	if err := os.WriteFile(iso, isoData, 0o600); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(root, "cover.png")
	writeCover(t, cover)

	builder := FPKGBuilder{Config: FPKGConfig{Emulator: emulatorTemplate(t), OutputDir: output, SupportDir: support}}
	if status := builder.Status(); !status.Ready {
		t.Fatalf("status = %+v", status)
	}
	game := Game{ID: "SLUS-20091", Title: "Fixture Game", ISOPath: iso, ISOFilename: "Game.iso", CoverPath: cover}

	stages := make([]FPKGState, 0, 8)
	path, err := builder.Build(context.Background(), game, func(progress FPKGProgress) {
		stages = append(stages, progress.Stage)
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != output || filepath.Base(path) != "Fixture Game_SLUS20091.pkg" {
		t.Fatalf("output path = %q", path)
	}
	if len(stages) == 0 || stages[len(stages)-1] != FPKGCompleted {
		t.Fatalf("stages = %v", stages)
	}

	converted, err := orbis.OpenPackage(path, "00000000000000000000000000000000")
	if err != nil {
		t.Fatalf("open converted package: %v", err)
	}
	defer converted.Close()
	if got := converted.Info().ContentID; got != "UP9000-SLUS20091_00-SLUS000000000001" {
		t.Fatalf("content ID = %q", got)
	}
	sfo, err := converted.ParamSFO()
	if err != nil {
		t.Fatal(err)
	}
	if got := sfo.String("TITLE"); got != "Fixture Game" {
		t.Fatalf("TITLE = %q", got)
	}
	if got := sfo.String("TITLE_ID"); got != "SLUS20091" {
		t.Fatalf("TITLE_ID = %q", got)
	}
	if got := sfo.String("CONTENT_ID"); got != "UP9000-SLUS20091_00-SLUS000000000001" {
		t.Fatalf("CONTENT_ID = %q", got)
	}

	disc := converted.Root().Find("image/disc01.iso")
	if disc == nil {
		t.Fatal("converted package is missing image/disc01.iso")
	}
	if disc.Size != int64(len(isoData)) {
		t.Fatalf("ISO size = %d, want %d", disc.Size, len(isoData))
	}
	stored, err := io.ReadAll(io.NewSectionReader(disc.Reader(), 0, disc.Size))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, isoData) {
		t.Fatal("stored ISO does not match the source image")
	}

	config := converted.Root().Find("config-emu-ps4.txt")
	if config == nil {
		t.Fatal("converted package is missing config-emu-ps4.txt")
	}
	configData, err := io.ReadAll(io.NewSectionReader(config.Reader(), 0, config.Size))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), "--max-disc-num=1") {
		t.Fatalf("emulator config was not patched: %q", string(configData))
	}
	for _, entry := range []string{"icon0.png", "save_data.png"} {
		data, err := converted.EntryByName(entry)
		if err != nil {
			t.Fatalf("read %s: %v", entry, err)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("%s is not a PNG: %v", entry, err)
		}
	}
	icon, err := converted.EntryByName("icon0.png")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(icon))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 512 || decoded.Bounds().Dy() != 512 {
		t.Fatalf("icon0.png is %v", decoded.Bounds())
	}

	if _, err := builder.Build(context.Background(), game, nil); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second build error = %v", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ps2-fpkg-") {
			t.Fatalf("workspace was not cleaned: %s", entry.Name())
		}
	}
}

func TestFPKGBuilderRejectsUnknownSerial(t *testing.T) {
	builder := FPKGBuilder{Config: FPKGConfig{Emulator: emulatorTemplate(t), OutputDir: t.TempDir()}}
	_, err := builder.Build(context.Background(), Game{ID: "unknown", Title: "Mystery"}, nil)
	if err == nil || !strings.Contains(err.Error(), "known serial") {
		t.Fatalf("error = %v", err)
	}
	if _, err := builder.Build(context.Background(), Game{ID: "SLUS-2009", Title: "Short"}, nil); err == nil || !strings.Contains(err.Error(), "unsupported PS2 serial") {
		t.Fatalf("short serial error = %v", err)
	}
}

func TestPatchEmulatorSFOUpdatesMetadata(t *testing.T) {
	sfo := &orbis.ParamSFO{}
	sfo.Set(orbis.SFOValue{Name: "CONTENT_ID", Type: orbis.SFOUtf8, Text: "UP9000-SLUS00000_00-SLUS000000000001", Max: 48})
	sfo.Set(orbis.SFOValue{Name: "TITLE_ID", Type: orbis.SFOUtf8, Text: "SLUS00000", Max: 12})
	sfo.Set(orbis.SFOValue{Name: "TITLE", Type: orbis.SFOUtf8, Text: "Markus95", Max: 128})

	contentID := patchContentID("UP9000-SLUS00000_00-SLUS000000000001", "SCES51719")
	if contentID != "UP9000-SCES51719_00-SLUS000000000001" {
		t.Fatalf("content ID = %q", contentID)
	}
	if err := patchEmulatorSFO(sfo, contentID, "SCES51719", "Gran Turismo 4"); err != nil {
		t.Fatal(err)
	}
	if got := sfo.String("CONTENT_ID"); got != contentID {
		t.Fatalf("CONTENT_ID = %q", got)
	}
	if got := sfo.String("TITLE_ID"); got != "SCES51719" {
		t.Fatalf("TITLE_ID = %q", got)
	}
	if got := sfo.String("TITLE"); got != "Gran Turismo 4" {
		t.Fatalf("TITLE = %q", got)
	}
	if err := patchEmulatorSFO(sfo, contentID, "SCES51719", strings.Repeat("x", 200)); err == nil {
		t.Fatal("expected an error for a title that does not fit")
	}
}

func TestFPKGStatusExplainsMissingTemplate(t *testing.T) {
	status := (FPKGBuilder{Config: FPKGConfig{Emulator: "missing.pkg", OutputDir: t.TempDir()}}).Status()
	if status.Ready || !strings.Contains(status.Message, "emulator FPKG") {
		t.Fatalf("status = %+v", status)
	}
	if status.Converter != "built-in" {
		t.Fatalf("converter = %q", status.Converter)
	}
}
