package orbis

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const testPasscode = "00000000000000000000000000000000"

// templatePath returns the bundled PS2 emulator package, or skips the test.
func templatePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "PS22PS4-GUI", "bin", "emulators", "JakV2.pkg")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("emulator template is not available: %v", err)
	}
	return path
}

// treeDigest walks a directory and returns "path:sha256" lines.
func treeDigest(t *testing.T, root string) []string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines = append(lines, relative+":"+hex.EncodeToString(digest.Sum(nil)))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	return lines
}

func TestReadEmulatorTemplate(t *testing.T) {
	reader, err := OpenPackage(templatePath(t), testPasscode)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	defer reader.Close()

	info := reader.Info()
	if info.ContentID != "UP9000-SLUS00000_00-SLUS000000000001" {
		t.Fatalf("unexpected content ID %q", info.ContentID)
	}
	sfo, err := reader.ParamSFO()
	if err != nil {
		t.Fatalf("param.sfo: %v", err)
	}
	if got := sfo.String("CONTENT_ID"); got != info.ContentID {
		t.Fatalf("param.sfo CONTENT_ID = %q, want %q", got, info.ContentID)
	}
	root := reader.Root()
	if root == nil {
		t.Fatal("template has no filesystem")
	}
	for _, want := range []string{"eboot.bin", "config-emu-ps4.txt", "sce_sys/keystone"} {
		if node := root.Find(want); node == nil {
			t.Fatalf("template is missing %s", want)
		}
	}

	// param.sfo must round trip through the parser byte for byte.
	original, err := reader.EntryData(entryParamSFO)
	if err != nil {
		t.Fatalf("read param.sfo entry: %v", err)
	}
	if serialized := sfo.Serialize(); string(serialized) != string(original) {
		t.Fatalf("param.sfo round trip changed %d bytes into %d", len(original), len(serialized))
	}
}

func TestExtractEmulatorTemplate(t *testing.T) {
	reader, err := OpenPackage(templatePath(t), testPasscode)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	defer reader.Close()
	destination := t.TempDir()
	if err := reader.ExtractFiles(destination); err != nil {
		t.Fatalf("extract: %v", err)
	}
	lines := treeDigest(t, destination)
	if len(lines) < 10 {
		t.Fatalf("extracted only %d files", len(lines))
	}
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "eboot.bin:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("eboot.bin missing from extraction: %v", lines)
	}
}

// TestRebuildTemplate extracts the emulator package, rebuilds it and checks that
// the rebuilt package still parses and carries the same filesystem.
func TestRebuildTemplate(t *testing.T) {
	source := templatePath(t)
	reader, err := OpenPackage(source, testPasscode)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	extracted := t.TempDir()
	if err := reader.ExtractFiles(extracted); err != nil {
		t.Fatalf("extract: %v", err)
	}
	sfo, err := reader.ParamSFO()
	if err != nil {
		t.Fatalf("param.sfo: %v", err)
	}
	contentID := reader.Info().ContentID
	reader.Close()

	if err := os.MkdirAll(filepath.Join(extracted, "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extracted, "sce_sys", "param.sfo"), sfo.Serialize(), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := DirTree(extracted)
	if err != nil {
		t.Fatalf("build tree: %v", err)
	}
	output := filepath.Join(t.TempDir(), "rebuilt.pkg")
	if err := Build(BuildOptions{
		ContentID:    contentID,
		Passcode:     testPasscode,
		Root:         root,
		Timestamp:    time.Unix(1659564000, 0).UTC(),
		CreationDate: time.Unix(1659564000, 0).UTC(),
	}, output, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	rebuilt, err := OpenPackage(output, testPasscode)
	if err != nil {
		t.Fatalf("open rebuilt package: %v", err)
	}
	defer rebuilt.Close()
	if rebuilt.Info().ContentID != contentID {
		t.Fatalf("rebuilt content ID = %q", rebuilt.Info().ContentID)
	}
	roundTrip := t.TempDir()
	if err := rebuilt.ExtractFiles(roundTrip); err != nil {
		t.Fatalf("extract rebuilt: %v", err)
	}
	before := treeDigest(t, extracted)
	after := treeDigest(t, roundTrip)
	// The rebuilt image also carries sce_sys/param.sfo, which lives in a PKG
	// entry in the original package.
	filtered := make([]string, 0, len(after))
	for _, line := range after {
		if strings.HasPrefix(line, filepath.Join("sce_sys", "param.sfo")+":") {
			continue
		}
		filtered = append(filtered, line)
	}
	beforeFiltered := make([]string, 0, len(before))
	for _, line := range before {
		if strings.HasPrefix(line, filepath.Join("sce_sys", "param.sfo")+":") {
			continue
		}
		beforeFiltered = append(beforeFiltered, line)
	}
	if len(filtered) != len(beforeFiltered) {
		t.Fatalf("rebuilt image has %d files, original had %d", len(filtered), len(beforeFiltered))
	}
	for i := range filtered {
		if filtered[i] != beforeFiltered[i] {
			t.Fatalf("file mismatch: %q != %q", filtered[i], beforeFiltered[i])
		}
	}
}
