package ps4

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"ps3mgr/internal/fpkgi"
)

// FPKGiPrefix is where the package server publishes FPKGi content lists.
const FPKGiPrefix = "/fpkgi/ps4/"

// FPKGiAliasPrefixes are other spellings of FPKGiPrefix the package server
// accepts, including the panel's export route, so a panel URL pasted against
// port 8081 still resolves.
var FPKGiAliasPrefixes = []string{"/api/ps4/fpkgi/", "/ps4/fpkgi/"}

// FPKGiCategories lists the FPKGi CONTENT_URLS categories a PS4 library can
// fill, in the order the panel offers them.
var FPKGiCategories = []string{"games", "updates", "dlc"}

// fpkgiCategory maps a scanned package format onto an FPKGi category. License
// packages are not installable content on their own and are left out.
func fpkgiCategory(format string) string {
	switch format {
	case "pkg-game", "pkg":
		return "games"
	case "pkg-patch":
		return "updates"
	case "pkg-dlc":
		return "dlc"
	}
	return ""
}

func validFPKGiCategory(category string) bool {
	for _, known := range FPKGiCategories {
		if category == known {
			return true
		}
	}
	return category == "all"
}

// FPKGiItems rescans the library and returns the packages in category
// ("games", "updates", "dlc", or "all") with download URLs on the package
// server. FPKGi downloads exactly one file per entry, so split packages that
// need Remote Package Installer to join their parts are skipped and counted.
func (s *Service) FPKGiItems(ctx context.Context, category string) ([]fpkgi.Item, int, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	if !validFPKGiCategory(category) {
		return nil, 0, fmt.Errorf("unknown FPKGi category %q (want games, updates, dlc, or all)", category)
	}
	if err := s.Content.AdvertiseError(); err != nil {
		return nil, 0, err
	}
	packages, err := s.LocalPackages(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	base := s.Content.AdvertiseURL
	items := make([]fpkgi.Item, 0, len(packages))
	skipped := 0
	for _, pkg := range packages {
		kind := fpkgiCategory(pkg.Format)
		if kind == "" || (category != "all" && kind != category) {
			continue
		}
		if len(pkg.Parts) != 1 {
			skipped++
			continue
		}
		relative, err := filepath.Rel(s.GameDir, pkg.Parts[0].Path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			skipped++
			continue
		}
		item := fpkgi.Item{
			URL: base + packageLibraryURL(relative), TitleID: pkg.TitleID, Name: pkg.Title,
			Region: pkg.Region, Version: pkg.Version, Size: pkg.Size,
		}
		if item.TitleID == "" {
			// FPKGi drops entries without a title ID; the content ID prefix is
			// the closest stable identifier an unrecognised header offers.
			item.TitleID = fallback(pkg.ContentID, "UNKNOWN")
		}
		if pkg.CoverPath != "" {
			item.CoverURL = base + FPKGiPrefix + "covers/" + pkg.ID + strings.ToLower(filepath.Ext(pkg.CoverPath))
		}
		items = append(items, item)
	}
	return items, skipped, nil
}

// FPKGiHandler serves /fpkgi/ps4/{category}.json and the covers they link to.
// The FPKGiAliasPrefixes forms, with or without .json, serve the same lists.
func (s *Service) FPKGiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, FPKGiPrefix)
		for _, prefix := range FPKGiAliasPrefixes {
			if alias, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
				name = strings.TrimSuffix(alias, ".json") + ".json"
			}
		}
		if strings.HasPrefix(name, "covers/") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, "covers/"), filepath.Ext(name))
			path, ok := s.Cover(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			http.ServeFile(w, r, path)
			return
		}
		category, ok := strings.CutSuffix(name, ".json")
		if !ok || !validFPKGiCategory(category) {
			http.NotFound(w, r)
			return
		}
		items, _, err := s.FPKGiItems(r.Context(), category)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		fpkgi.Write(w, r, items, "")
	})
}
