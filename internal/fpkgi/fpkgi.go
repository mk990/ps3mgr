// Package fpkgi renders content lists in the JSON format read by FPKGi, the
// PS4/PS5 homebrew package installer (https://github.com/ItsJokerZz/FPKGi).
//
// FPKGi keys every entry by the direct download URL of its package and
// discards entries without a title ID, name, or size, so those three are
// always written. Optional fields FPKGi understands as unknown are written as
// JSON null rather than empty strings.
package fpkgi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Item is one downloadable package in an FPKGi content list.
type Item struct {
	URL      string
	TitleID  string
	Name     string
	Region   string
	Version  string
	Release  string
	MinFW    string
	CoverURL string
	Size     int64
}

type entry struct {
	TitleID  string  `json:"title_id"`
	Region   *string `json:"region"`
	Name     string  `json:"name"`
	Version  *string `json:"version"`
	Release  *string `json:"release"`
	Size     int64   `json:"size"`
	MinFW    *string `json:"min_fw"`
	CoverURL *string `json:"cover_url"`
}

// Document is the top-level FPKGi content file.
type Document struct {
	DATA map[string]entry `json:"DATA"`
}

// Build converts items into an FPKGi document. Items without a URL are
// skipped because FPKGi has nothing to download for them.
func Build(items []Item) Document {
	document := Document{DATA: make(map[string]entry, len(items))}
	for _, item := range items {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		document.DATA[item.URL] = entry{
			TitleID:  item.TitleID,
			Region:   optional(Region(item.Region)),
			Name:     item.Name,
			Version:  optional(item.Version),
			Release:  optional(item.Release),
			Size:     item.Size,
			MinFW:    optional(item.MinFW),
			CoverURL: optional(item.CoverURL),
		}
	}
	return document
}

// Marshal renders items as an indented FPKGi document. Map keys are sorted by
// encoding/json, so the output is stable for identical input.
func Marshal(items []Item) ([]byte, error) {
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].URL < sorted[j].URL })
	data, err := json.MarshalIndent(Build(sorted), "", "    ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Region maps ps3mgr and content-ID region codes onto the four regions FPKGi
// filters by. Anything else is reported as unknown.
func Region(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "US", "UP", "USA":
		return "USA"
	case "EU", "EP", "EUR":
		return "EUR"
	case "JP", "JAP", "JPN":
		return "JAP"
	case "ASIA", "HP", "KP":
		return "ASIA"
	}
	return ""
}

func optional(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// Write sends items as an FPKGi document. A non-empty filename makes the
// response a download instead of an inline JSON body.
func Write(w http.ResponseWriter, r *http.Request, items []Item, filename string) {
	data, err := Marshal(items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if filename != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
