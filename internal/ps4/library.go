package ps4

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var ErrInvalidPKG = errors.New("invalid PS4 PKG")

var (
	contentIDPattern = regexp.MustCompile(`(?i)[A-Z]{2}[0-9]{4}-CUSA[0-9]{5}_00-[A-Z0-9]{16}`)
	titleIDPattern   = regexp.MustCompile(`(?i)CUSA[0-9]{5}`)
	partPattern      = regexp.MustCompile(`(?i)(?:[._-](?:part)?)([0-9]{1,3})$`)
	versionPattern   = regexp.MustCompile(`(?i)[._-]V([0-9]{2})([0-9]{2})(?:[._-]|$)`)
)

type pkgMetadata struct {
	path, name, title, contentID, titleID, format, version, region, group string
	size                                                                  int64
	part                                                                  int
}

type Library struct{}

func (Library) Scan(ctx context.Context, root string) ([]Package, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("PS4 game directory is not configured")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("PS4 game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("PS4 game directory is not a directory: %s", root)
	}
	groups := make(map[string][]pkgMetadata)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".pkg") {
			return nil
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		metadata, err := inspectPackage(path, filepath.Dir(relative))
		if err != nil {
			if errors.Is(err, ErrInvalidPKG) {
				return nil
			}
			return fmt.Errorf("inspect PS4 package %q: %w", path, err)
		}
		groups[metadata.group] = append(groups[metadata.group], metadata)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]Package, 0, len(groups))
	for key, values := range groups {
		sort.Slice(values, func(i, j int) bool {
			if values[i].part != values[j].part {
				return values[i].part < values[j].part
			}
			return strings.ToLower(values[i].name) < strings.ToLower(values[j].name)
		})
		first := values[0]
		pkg := Package{ID: publicID(key), ContentID: first.contentID, TitleID: first.titleID, Title: first.title, Format: first.format, Version: first.version, Region: first.region}
		for _, value := range values {
			pkg.Size += value.size
			pkg.Parts = append(pkg.Parts, PackagePart{Name: value.name, Size: value.size, Path: value.path})
		}
		result = append(result, pkg)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
	})
	return result, nil
}

func inspectPackage(path, groupScope string) (pkgMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return pkgMetadata{}, err
	}
	defer file.Close()
	header := make([]byte, 0x100)
	if _, err := io.ReadFull(file, header); err != nil {
		return pkgMetadata{}, fmt.Errorf("%w: truncated header", ErrInvalidPKG)
	}
	if binary.BigEndian.Uint32(header[:4]) != 0x7f434e54 {
		return pkgMetadata{}, fmt.Errorf("%w: bad magic", ErrInvalidPKG)
	}
	info, err := file.Stat()
	if err != nil {
		return pkgMetadata{}, err
	}
	contentID := findContentID(header)
	titleID := strings.ToUpper(titleIDPattern.FindString(contentID))
	if titleID == "" {
		titleID = strings.ToUpper(titleIDPattern.FindString(string(header)))
	}
	format := packageFormat(header)
	base := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
	part, groupName := splitPart(base)
	title := cleanTitle(groupName, titleID)
	group := strings.ToLower(groupScope) + "|" + strings.ToUpper(contentID) + "|" + format + "|" + strings.ToLower(groupName)
	version := ""
	if match := versionPattern.FindStringSubmatch(base); len(match) == 3 {
		version = match[1] + "." + match[2]
	}
	return pkgMetadata{path: path, name: info.Name(), title: title, contentID: strings.ToUpper(contentID), titleID: titleID, format: format, version: version, region: packageRegion(contentID), group: group, size: info.Size(), part: part}, nil
}

func findContentID(header []byte) string {
	for _, offset := range []int{0x40, 0x30} {
		if len(header) >= offset+0x30 {
			if found := contentIDPattern.Find(header[offset : offset+0x30]); found != nil {
				return string(found)
			}
		}
	}
	return string(contentIDPattern.Find(header))
}

func packageFormat(header []byte) string {
	if len(header) < 0x78 {
		return "pkg"
	}
	switch binary.BigEndian.Uint32(header[0x74:0x78]) {
	case 0x1a:
		return "pkg-game"
	case 0x1b:
		return "pkg-dlc"
	case 0x1c:
		return "pkg-license"
	case 0x1e:
		return "pkg-patch"
	default:
		return "pkg"
	}
}

func splitPart(base string) (int, string) {
	match := partPattern.FindStringSubmatchIndex(base)
	if len(match) == 4 {
		var part int
		_, _ = fmt.Sscanf(base[match[2]:match[3]], "%d", &part)
		return part, strings.TrimRight(base[:match[0]], "._-")
	}
	return 0, base
}

func cleanTitle(value, titleID string) string {
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, ".", " ")
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if titleID != "" {
		value = strings.TrimSpace(regexp.MustCompile(`(?i)`+regexp.QuoteMeta(titleID)).ReplaceAllString(value, ""))
	}
	if value == "" {
		return fallback(titleID, "Unknown PS4 package")
	}
	return value
}

func packageRegion(contentID string) string {
	if len(contentID) < 2 {
		return ""
	}
	switch strings.ToUpper(contentID[:2]) {
	case "UP":
		return "US"
	case "EP":
		return "EU"
	case "JP":
		return "JP"
	case "HP":
		return "ASIA"
	default:
		return strings.ToUpper(contentID[:2])
	}
}

func publicID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}
