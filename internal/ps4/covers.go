package ps4

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ps3mgr/internal/pkgicon"
)

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
		if errors.Is(err, pkgicon.ErrNotFound) {
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
	icon, extension, err := pkgicon.Find(ctx, file, info.Size())
	if err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(icon)
	return data, extension, err
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
