package ps4

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	icon0EntryID    = 0x1200
	maxIcon0Bytes   = 20 << 20
	maxTableEntries = 1 << 16
)

var errIcon0NotFound = errors.New("PS4 package does not contain icon0")

type CoverCache struct{}

type CoverStatus struct {
	GameDir   string `json:"game_dir"`
	CacheDir  string `json:"cache_dir"`
	Available bool   `json:"available"`
	Writable  bool   `json:"writable"`
	Images    int    `json:"images"`
	Error     string `json:"error,omitempty"`
}

type CoverFailure struct {
	TitleID string `json:"title_id,omitempty"`
	Reason  string `json:"reason"`
}

func (c *CoverCache) Ensure(root string) (string, error) {
	cacheRoot := filepath.Join(root, "covers")
	info, err := os.Stat(root)
	if err != nil {
		return cacheRoot, fmt.Errorf("access PS4 game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return cacheRoot, fmt.Errorf("PS4 game path is not a directory: %s", root)
	}
	if err = os.MkdirAll(cacheRoot, 0o755); err != nil {
		return cacheRoot, fmt.Errorf("create PS4 cover cache %q: %w", cacheRoot, err)
	}
	probe, err := os.CreateTemp(cacheRoot, ".ps3mgr-write-test-*")
	if err != nil {
		return cacheRoot, fmt.Errorf("PS4 cover cache is not writable %q: %w", cacheRoot, err)
	}
	probePath := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probePath)
		return cacheRoot, fmt.Errorf("verify PS4 cover cache %q: %w", cacheRoot, closeErr)
	}
	if err = os.Remove(probePath); err != nil {
		return cacheRoot, fmt.Errorf("clean PS4 cover cache probe %q: %w", probePath, err)
	}
	return cacheRoot, nil
}

func (c *CoverCache) Status(root string) CoverStatus {
	status := CoverStatus{GameDir: root, CacheDir: filepath.Join(root, "covers")}
	cacheRoot, err := c.Ensure(root)
	status.CacheDir = cacheRoot
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Available, status.Writable = true, true
	status.Images = len(indexPS4Covers(cacheRoot))
	return status
}

// Populate prefers manually supplied local artwork and otherwise extracts the
// package's embedded icon0. It never contacts an external cover service.
func (c *CoverCache) Populate(ctx context.Context, root string, packages []Package) (int, []CoverFailure) {
	cacheRoot, err := c.Ensure(root)
	if err != nil {
		return 0, []CoverFailure{{Reason: err.Error()}}
	}
	covers := indexPS4Covers(cacheRoot)
	extracted := 0
	failures := make([]CoverFailure, 0)
	for i := range packages {
		if err := ctx.Err(); err != nil {
			return extracted, append(failures, CoverFailure{Reason: err.Error()})
		}
		if cover := findPS4Cover(packages[i], covers); cover != "" {
			setPS4Cover(&packages[i], cover)
			continue
		}
		if len(packages[i].Parts) == 0 {
			continue
		}
		data, extension, err := extractPackageIcon(ctx, packages[i].Parts[0].Path)
		if errors.Is(err, errIcon0NotFound) {
			continue
		}
		if err != nil {
			failures = append(failures, CoverFailure{TitleID: packages[i].TitleID, Reason: err.Error()})
			continue
		}
		key := packages[i].TitleID
		if key == "" {
			key = packages[i].ID
		}
		destination := filepath.Join(cacheRoot, key+extension)
		if err = writeCoverAtomically(destination, data); err != nil {
			failures = append(failures, CoverFailure{TitleID: packages[i].TitleID, Reason: err.Error()})
			continue
		}
		covers[coverKey(key)] = destination
		setPS4Cover(&packages[i], destination)
		extracted++
	}
	return extracted, failures
}

func extractPackageIcon(ctx context.Context, path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open PS4 package for icon: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	header := make([]byte, 32)
	if _, err = io.ReadFull(file, header); err != nil || binary.BigEndian.Uint32(header[:4]) != 0x7f434e54 {
		return nil, "", errIcon0NotFound
	}
	total := binary.BigEndian.Uint32(header[16:20])
	tableOffset := binary.BigEndian.Uint32(header[24:28])
	if total == 0 || total > maxTableEntries || uint64(tableOffset)+uint64(total)*32 > uint64(info.Size()) {
		return nil, "", errIcon0NotFound
	}
	entry := make([]byte, 32)
	for index := uint32(0); index < total; index++ {
		if err = ctx.Err(); err != nil {
			return nil, "", err
		}
		if _, err = file.ReadAt(entry, int64(tableOffset)+int64(index)*32); err != nil {
			return nil, "", fmt.Errorf("read PS4 package table: %w", err)
		}
		if binary.BigEndian.Uint32(entry[:4]) != icon0EntryID {
			continue
		}
		offset, size := binary.BigEndian.Uint32(entry[16:20]), binary.BigEndian.Uint32(entry[20:24])
		if size == 0 || size > maxIcon0Bytes || uint64(offset)+uint64(size) > uint64(info.Size()) {
			return nil, "", fmt.Errorf("invalid icon0 entry in %s", filepath.Base(path))
		}
		data := make([]byte, size)
		if _, err = file.ReadAt(data, int64(offset)); err != nil {
			return nil, "", fmt.Errorf("read icon0 from %s: %w", filepath.Base(path), err)
		}
		switch http.DetectContentType(data) {
		case "image/png":
			return data, ".png", nil
		case "image/jpeg":
			return data, ".jpg", nil
		case "image/webp":
			return data, ".webp", nil
		default:
			return nil, "", fmt.Errorf("icon0 in %s is not a supported image", filepath.Base(path))
		}
	}
	return nil, "", errIcon0NotFound
}

func writeCoverAtomically(destination string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".ps4-cover-*.partial")
	if err != nil {
		return fmt.Errorf("create PS4 cover cache file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o644); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("write PS4 cover cache: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close PS4 cover cache: %w", closeErr)
	}
	if err = os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("commit PS4 cover cache: %w", err)
	}
	return nil
}

func findPS4Cover(pkg Package, covers map[string]string) string {
	if len(pkg.Parts) > 0 {
		base := strings.TrimSuffix(pkg.Parts[0].Path, filepath.Ext(pkg.Parts[0].Path))
		for _, candidate := range ps4ImageCandidates(base) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	for _, key := range []string{pkg.TitleID, pkg.ContentID, pkg.ID} {
		if cover := covers[coverKey(key)]; cover != "" {
			return cover
		}
	}
	return ""
}

func indexPS4Covers(root string) map[string]string {
	result := make(map[string]string)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isPS4CoverExtension(filepath.Ext(entry.Name())) {
			return nil
		}
		key := coverKey(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if key != "" && result[key] == "" {
			result[key] = path
		}
		return nil
	})
	return result
}

func ps4ImageCandidates(base string) []string {
	return []string{base + ".png", base + ".jpg", base + ".jpeg", base + ".webp", base + ".PNG", base + ".JPG", base + ".JPEG", base + ".WEBP"}
}

func isPS4CoverExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func coverKey(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
}

func setPS4Cover(pkg *Package, path string) {
	pkg.CoverPath = path
	pkg.CoverURL = "/api/ps4/games/" + pkg.ID + "/cover"
}
