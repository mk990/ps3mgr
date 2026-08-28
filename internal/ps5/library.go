package ps5

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/games"
)

var titleIDPattern = regexp.MustCompile(`(?i)(PPSA|CUSA)[-_ ]?(\d{5})`)

var imageFormats = map[string]string{
	".ffpfsc": "ffpfsc",
	".exfat":  "exfat",
	".ffpkg":  "ffpkg",
	".ffpfs":  "ffpfs",
}

type Library struct{}

func (Library) Scan(ctx context.Context, root string) ([]domain.Game, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("PS5 game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("PS5 game directory is not a directory: %s", root)
	}
	result := make([]domain.Game, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if strings.EqualFold(entry.Name(), "backports") || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			paramPath := filepath.Join(path, "sce_sys", "param.json")
			if paramInfo, statErr := os.Stat(paramPath); statErr == nil && paramInfo.Mode().IsRegular() {
				game, gameErr := folderGame(ctx, path, paramPath)
				if gameErr != nil {
					return gameErr
				}
				result = append(result, game)
				return filepath.SkipDir
			}
			return nil
		}
		format, supported := imageFormats[strings.ToLower(filepath.Ext(entry.Name()))]
		if !supported || !entry.Type().IsRegular() {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		id := titleID(entry.Name())
		title := titleFromFilename(entry.Name(), id)
		game := domain.Game{
			ID: id, Title: title, Format: format, LocalPath: path, Size: fileInfo.Size(),
			State: domain.StateNotInstalled,
		}
		game.IconURL = ""
		result = append(result, game)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan PS5 game directory %q: %w", root, err)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func folderGame(ctx context.Context, directory, paramPath string) (domain.Game, error) {
	metadata, err := readParam(paramPath)
	if err != nil {
		return domain.Game{}, fmt.Errorf("read PS5 metadata %q: %w", paramPath, err)
	}
	id := metadata.ID
	if id == "" {
		id = titleID(filepath.Base(directory))
	}
	title := strings.TrimSpace(metadata.Title)
	if title == "" {
		title = filepath.Base(directory)
	}
	size, err := directorySize(ctx, directory)
	if err != nil {
		return domain.Game{}, fmt.Errorf("measure PS5 game %q: %w", title, err)
	}
	game := domain.Game{ID: id, Title: title, Format: "folder", LocalPath: directory, Size: size, State: domain.StateNotInstalled}
	for _, name := range []string{"icon0.png", "icon0.PNG"} {
		icon := filepath.Join(directory, "sce_sys", name)
		if info, iconErr := os.Stat(icon); iconErr == nil && info.Mode().IsRegular() {
			game.IconPath = icon
			game.IconURL = "/api/ps5/games/" + games.PublicID(game) + "/icon"
			break
		}
	}
	return game, nil
}

type paramMetadata struct {
	ID    string
	Title string
}

func readParam(path string) (paramMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return paramMetadata{}, err
	}
	if len(data) > 1<<20 {
		return paramMetadata{}, fmt.Errorf("param.json exceeds 1 MiB")
	}
	var value map[string]any
	if err = json.Unmarshal(data, &value); err != nil {
		return paramMetadata{}, err
	}
	id := firstString(value, "titleId", "title_id")
	title := ""
	if localized, ok := value["localizedParameters"].(map[string]any); ok {
		if english, ok := localized["en-US"].(map[string]any); ok {
			title = firstString(english, "titleName")
		}
		if title == "" {
			for _, locale := range localized {
				if fields, ok := locale.(map[string]any); ok {
					title = firstString(fields, "titleName")
					if title != "" {
						break
					}
				}
			}
		}
	}
	if title == "" {
		title = firstString(value, "titleName")
	}
	return paramMetadata{ID: normalizeTitleID(id), Title: title}, nil
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func titleID(value string) string {
	match := titleIDPattern.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	return strings.ToUpper(match[1] + match[2])
}

func normalizeTitleID(value string) string {
	if id := titleID(value); id != "" {
		return id
	}
	return strings.ToUpper(strings.TrimSpace(value))
}

func titleFromFilename(filename, id string) string {
	title := strings.TrimSuffix(filename, filepath.Ext(filename))
	if match := titleIDPattern.FindStringIndex(title); match != nil {
		title = strings.Trim(strings.TrimSpace(title[:match[0]]+" "+title[match[1]:]), "-_. ")
	}
	if title == "" {
		if id != "" {
			return id
		}
		return strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	return title
}

func directorySize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
