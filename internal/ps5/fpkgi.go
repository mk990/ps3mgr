package ps5

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ps3mgr/internal/fpkgi"
)

// FPKGiPrefix is where the package server publishes the PS5 FPKGi content
// list and the files it links to.
const FPKGiPrefix = "/fpkgi/ps5/"

var (
	contentIDPattern = regexp.MustCompile(`(?i)([A-Z]{2})[0-9]{4}-(?:PPSA|CUSA)[0-9]{5}_00-[A-Z0-9]{16}`)
	pkgVersionRegexp = regexp.MustCompile(`(?i)[._ -]V([0-9]{1,2})[._]?([0-9]{2})(?:[^0-9]|$)`)
	coverExtensions  = []string{".png", ".jpg", ".jpeg"}
)

// Advertiser reports the LAN base URL the console downloads from, or why none
// is usable.
type Advertiser func() (string, error)

// FPKGiItems lists the installable .pkg files below the PS5 library. FPKGi
// installs packages only, so ShadowMountPlus folders and images are not part
// of the list. A cover is any image beside the package with the same base name.
func (s *Service) FPKGiItems(ctx context.Context, advertise Advertiser) ([]fpkgi.Item, error) {
	base, err := advertise()
	if err != nil {
		return nil, err
	}
	root := s.GameDir
	items := make([]fpkgi.Item, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if strings.HasPrefix(entry.Name(), ".") || !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pkg") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		items = append(items, pkgItem(base, root, relative, info.Size()))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan PS5 packages %q: %w", root, err)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

func pkgItem(base, root, relative string, size int64) fpkgi.Item {
	filename := filepath.Base(relative)
	contentID := contentIDPattern.FindString(filename)
	if contentID == "" {
		contentID = headerContentID(filepath.Join(root, relative))
	}
	id := titleID(contentID)
	if id == "" {
		id = titleID(filename)
	}
	name := contentIDPattern.ReplaceAllString(filename, "")
	name = titleFromFilename(strings.ReplaceAll(name, "_", " "), id)
	item := fpkgi.Item{URL: base + fpkgiURL("pkg", relative), TitleID: fallbackString(id, "UNKNOWN"), Name: name, Size: size}
	if contentID != "" {
		item.Region = strings.ToUpper(contentID[:2])
	}
	if match := pkgVersionRegexp.FindStringSubmatch(filename); len(match) == 3 {
		item.Version = fmt.Sprintf("%02s.%s", match[1], match[2])
	}
	stem := strings.TrimSuffix(relative, filepath.Ext(relative))
	for _, extension := range coverExtensions {
		if info, err := os.Stat(filepath.Join(root, stem+extension)); err == nil && info.Mode().IsRegular() {
			item.CoverURL = base + fpkgiURL("covers", stem+extension)
			break
		}
	}
	return item
}

// headerContentID reads the content ID from the start of a package. Both PS4
// and PS5 fake packages store it within the first few hundred bytes.
func headerContentID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	header := make([]byte, 0x1000)
	n, _ := io.ReadFull(file, header)
	return string(contentIDPattern.Find(header[:n]))
}

func fpkgiURL(kind, relative string) string {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return FPKGiPrefix + kind + "/" + strings.Join(parts, "/")
}

// FPKGiHandler serves /fpkgi/ps5/ps5.json and the packages and covers it
// links to. Only .pkg and image files inside the PS5 library are reachable.
func (s *Service) FPKGiHandler(advertise Advertiser) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.EscapedPath(), FPKGiPrefix)
		switch {
		case name == "ps5.json":
			items, err := s.FPKGiItems(r.Context(), advertise)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			fpkgi.Write(w, r, items, "")
		case strings.HasPrefix(name, "pkg/"):
			s.serveLibraryFile(w, r, strings.TrimPrefix(name, "pkg/"), []string{".pkg"}, "application/octet-stream")
		case strings.HasPrefix(name, "covers/"):
			s.serveLibraryFile(w, r, strings.TrimPrefix(name, "covers/"), coverExtensions, "")
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Service) serveLibraryFile(w http.ResponseWriter, r *http.Request, escaped string, extensions []string, contentType string) {
	path, err := resolveLibraryFile(s.GameDir, escaped, extensions)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "file unavailable", http.StatusGone)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "file unavailable", http.StatusGone)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// resolveLibraryFile maps an escaped URL path onto a regular file inside root,
// rejecting traversal, symlink escapes, hidden entries, and other extensions.
func resolveLibraryFile(configuredRoot, escaped string, extensions []string) (string, error) {
	relativeURL, err := url.PathUnescape(escaped)
	if err != nil || relativeURL == "" {
		return "", fmt.Errorf("invalid path")
	}
	relative := filepath.Clean(filepath.FromSlash(relativeURL))
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path")
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("invalid path")
		}
	}
	allowed := false
	for _, extension := range extensions {
		allowed = allowed || strings.EqualFold(filepath.Ext(relative), extension)
	}
	if !allowed {
		return "", fmt.Errorf("invalid path")
	}
	root, err := filepath.Abs(configuredRoot)
	if err != nil {
		return "", err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(root, path)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("outside the PS5 library")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("file unavailable")
	}
	return path, nil
}

func fallbackString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
