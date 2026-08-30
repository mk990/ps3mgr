package orbis

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLargeImageRoundTrip builds a package big enough to need indirect and
// doubly-indirect block pointers, which is the normal case for a real PS2 ISO.
func TestLargeImageRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 150 MiB package")
	}
	out := t.TempDir()
	reader, err := OpenPackage(templatePath(t), testPasscode)
	if err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(out, "extract")
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reader.ExtractFiles(extracted); err != nil {
		t.Fatal(err)
	}
	sfo, err := reader.ParamSFO()
	if err != nil {
		t.Fatal(err)
	}
	contentID := reader.Info().ContentID
	reader.Close()
	if err := os.MkdirAll(filepath.Join(extracted, "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extracted, "sce_sys", "param.sfo"), sfo.Serialize(), 0o644); err != nil {
		t.Fatal(err)
	}

	// A pseudo-random ISO large enough to need ib[0] and ib[1] pointers.
	isoPath := filepath.Join(out, "big.iso")
	const isoSize = 150 << 20
	file, err := os.Create(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(file, rand.New(rand.NewSource(42)), isoSize); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	isoDigest := fileDigest(t, isoPath)

	tree, err := DirTree(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.AddFile("image/disc01.iso", isoPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(out, "big.pkg")
	start := time.Now()
	if err := Build(BuildOptions{
		ContentID: contentID, Passcode: testPasscode, Root: tree,
		Timestamp: time.Unix(1659564000, 0).UTC(), CreationDate: time.Unix(1659564000, 0).UTC(),
	}, target, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("built a %d byte package in %s", isoSize, time.Since(start))

	built, err := OpenPackage(target, testPasscode)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	node := built.Root().Find("image/disc01.iso")
	if node == nil {
		t.Fatal("missing disc01.iso")
	}
	if node.Size != isoSize {
		t.Fatalf("size = %d", node.Size)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(node.Reader(), 0, node.Size)); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != isoDigest {
		t.Fatalf("ISO digest mismatch: %s != %s", got, isoDigest)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
