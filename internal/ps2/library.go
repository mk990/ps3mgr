package ps2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var gameIDPattern = regexp.MustCompile(`(?i)(S[CL][A-Z]{2}|PBPX|PAPX|PCPX|TCES|TCUS)[-_ .]?(\d{3})[. _-]?(\d{2})`)

type Library struct{}

func (Library) Scan(ctx context.Context, root string) ([]Game, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("PS2 game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("PS2 game directory is not a directory: %s", root)
	}
	coversRoot := filepath.Clean(filepath.Join(root, "covers"))
	covers := indexCovers(coversRoot)
	result := make([]Game, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if entry.IsDir() {
			if filepath.Clean(path) == coversRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".iso") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		id := identifyISO(path)
		title := strings.TrimSpace(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if match := gameIDPattern.FindStringIndex(title); match != nil {
			title = strings.Trim(strings.TrimSpace(title[:match[0]]+" "+title[match[1]:]), ".-_ ")
		}
		if title == "" {
			title = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		public := stableID(path, root)
		cover := findCover(path, id, covers)
		game := Game{ID: id, PublicID: public, Title: title, ISOPath: path, ISOFilename: entry.Name(), Size: info.Size(), CoverPath: cover, OPLReady: id != "unknown"}
		if cover != "" {
			game.CoverURL = "/api/ps2/games/" + public + "/cover"
		}
		result = append(result, game)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan PS2 game directory %q: %w", root, err)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func identifyISO(path string) string {
	if id, err := gameIDFromISO(path); err == nil && id != "" {
		return id
	}
	if match := gameIDPattern.FindStringSubmatch(filepath.Base(path)); match != nil {
		return normalizeGameID(match)
	}
	return "unknown"
}

func normalizeGameID(match []string) string {
	return strings.ToUpper(match[1]) + "-" + match[2] + match[3]
}

// gameIDFromISO reads the ISO9660 root directory and SYSTEM.CNF without mounting the image.
func gameIDFromISO(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	pvd := make([]byte, 2048)
	if _, err = f.ReadAt(pvd, 16*2048); err != nil {
		return "", err
	}
	if string(pvd[1:6]) != "CD001" {
		return "", fmt.Errorf("not ISO9660")
	}
	root := pvd[156:]
	if len(root) < 34 || int(root[0]) < 34 {
		return "", fmt.Errorf("invalid root record")
	}
	extent := int64(uint32(root[2]) | uint32(root[3])<<8 | uint32(root[4])<<16 | uint32(root[5])<<24)
	size := int64(uint32(root[10]) | uint32(root[11])<<8 | uint32(root[12])<<16 | uint32(root[13])<<24)
	if size <= 0 || size > 16<<20 {
		return "", fmt.Errorf("invalid root size")
	}
	dir := make([]byte, size)
	if _, err = f.ReadAt(dir, extent*2048); err != nil && err != io.EOF {
		return "", err
	}
	for offset := 0; offset < len(dir); {
		length := int(dir[offset])
		if length == 0 {
			offset = (offset/2048 + 1) * 2048
			continue
		}
		if offset+length > len(dir) || length < 34 {
			break
		}
		record := dir[offset : offset+length]
		nameLen := int(record[32])
		if 33+nameLen <= len(record) {
			name := strings.ToUpper(string(record[33 : 33+nameLen]))
			if strings.TrimSuffix(name, ";1") == "SYSTEM.CNF" {
				cnfExtent := int64(uint32(record[2]) | uint32(record[3])<<8 | uint32(record[4])<<16 | uint32(record[5])<<24)
				cnfSize := int64(uint32(record[10]) | uint32(record[11])<<8 | uint32(record[12])<<16 | uint32(record[13])<<24)
				if cnfSize <= 0 || cnfSize > 65536 {
					return "", fmt.Errorf("invalid SYSTEM.CNF size")
				}
				data := make([]byte, cnfSize)
				if _, err = f.ReadAt(data, cnfExtent*2048); err != nil && err != io.EOF {
					return "", err
				}
				if match := gameIDPattern.FindStringSubmatch(string(data)); match != nil {
					return normalizeGameID(match), nil
				}
			}
		}
		offset += length
	}
	return "", fmt.Errorf("game ID not found")
}

func stableID(path, root string) string {
	rel, _ := filepath.Rel(root, path)
	sum := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	return hex.EncodeToString(sum[:8])
}

func findCover(iso, id string, covers map[string]string) string {
	base := strings.TrimSuffix(iso, filepath.Ext(iso))
	for _, candidate := range imageCandidates(base) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if id != "unknown" {
		for _, candidate := range imageCandidates(filepath.Join(filepath.Dir(iso), id)) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
		if cover := covers[coverKey(id)]; cover != "" {
			return cover
		}
	}
	return ""
}

func imageCandidates(base string) []string {
	extensions := []string{".jpg", ".jpeg", ".png", ".webp", ".JPG", ".JPEG", ".PNG", ".WEBP"}
	result := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		result = append(result, base+extension)
	}
	return result
}

// indexCovers indexes user-supplied cover files. It never downloads or mutates
// anything. Direct files take priority, followed by the ps2-covers repository's
// default and 3d directories, then any other nested directory.
func indexCovers(root string) map[string]string {
	result := make(map[string]string)
	addDirectoryCovers(result, root)
	addDirectoryCovers(result, filepath.Join(root, "default"))
	addDirectoryCovers(result, filepath.Join(root, "3d"))
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isCoverExtension(filepath.Ext(entry.Name())) {
			return nil
		}
		addCover(result, path)
		return nil
	})
	return result
}

func addDirectoryCovers(covers map[string]string, directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isCoverExtension(filepath.Ext(entry.Name())) {
			continue
		}
		addCover(covers, filepath.Join(directory, entry.Name()))
	}
}

func addCover(covers map[string]string, path string) {
	key := coverKey(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if key != "" && covers[key] == "" {
		covers[key] = path
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

func isCoverExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}
