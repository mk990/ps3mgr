package games

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"ps3mgr/internal/domain"
)

type Library struct{}

func NewLibrary() *Library { return &Library{} }

func (l *Library) Scan(ctx context.Context, root string) ([]domain.Game, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("access local game directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local game path %q is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read local game directory %q: %w", root, err)
	}
	result := make([]domain.Game, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		gamePath := filepath.Join(root, entry.Name())
		game := domain.Game{Title: entry.Name(), LocalPath: gamePath, State: domain.StateNotInstalled}
		metaPath := filepath.Join(gamePath, "PS3_GAME", "PARAM.SFO")
		if data, readErr := os.ReadFile(metaPath); readErr == nil {
			if meta, parseErr := ParseSFO(data); parseErr == nil {
				game.Title = first(meta["TITLE"], game.Title)
				game.ID = meta["TITLE_ID"]
				game.Version = meta["APP_VER"]
				game.Region = region(game.ID)
			}
		}
		iconPath := filepath.Join(gamePath, "PS3_GAME", "ICON0.PNG")
		if icon, iconErr := os.Stat(iconPath); iconErr == nil && !icon.IsDir() {
			game.IconPath = iconPath
		}
		game.Size, err = directorySize(ctx, gamePath)
		if err != nil {
			return nil, fmt.Errorf("measure game %q: %w", entry.Name(), err)
		}
		game.IconURL = "/api/local-games/" + PublicID(game) + "/icon"
		result = append(result, game)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func directorySize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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

func Compare(local, remote []domain.Game) []domain.Game {
	byID, byTitle := make(map[string]bool), make(map[string]bool)
	for _, game := range remote {
		if game.ID != "" {
			byID[strings.ToUpper(game.ID)] = true
		}
		byTitle[NormalizeTitle(game.Title)] = true
	}
	result := append([]domain.Game(nil), local...)
	for i := range result {
		installed := result[i].ID != "" && byID[strings.ToUpper(result[i].ID)]
		if !installed {
			installed = byTitle[NormalizeTitle(result[i].Title)]
		}
		result[i].Installed = installed
		if installed {
			result[i].State = domain.StateInstalled
		} else {
			result[i].State = domain.StateNotInstalled
		}
	}
	return result
}

func NormalizeTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func PublicID(game domain.Game) string {
	if game.ID != "" {
		return strings.ToUpper(game.ID)
	}
	return NormalizeTitle(game.Title)
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func region(id string) string {
	upper := strings.ToUpper(id)
	switch {
	case strings.HasPrefix(upper, "BLES"), strings.HasPrefix(upper, "BCES"):
		return "Europe"
	case strings.HasPrefix(upper, "BLUS"), strings.HasPrefix(upper, "BCUS"):
		return "North America"
	case strings.HasPrefix(upper, "BLJM"), strings.HasPrefix(upper, "BCJS"):
		return "Japan"
	default:
		return ""
	}
}
