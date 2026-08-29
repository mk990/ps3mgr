package ps4

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ContentServer struct {
	Listen       string
	AdvertiseURL string
	Root         string

	mu       sync.RWMutex
	server   *http.Server
	listener net.Listener
	files    map[string]servedPackage
}

type servedPackage struct {
	path string
	name string
}

type indexedPackage struct {
	Name string
	URL  string
	Size string
}

var packageIndexTemplate = template.Must(template.New("ps4-package-index").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>PS4 Package Index</title><style>body{font:16px system-ui,sans-serif;max-width:70rem;margin:auto;padding:1rem;background:#111827;color:#f9fafb}a{color:#93c5fd;overflow-wrap:anywhere}li{margin:.8rem 0}small{color:#9ca3af}</style></head>
<body><h1>PS4 Package Index</h1><p>{{len .}} package file(s) under the configured PS4 library.</p><ul>{{range .}}<li><a href="{{.URL}}">{{.Name}}</a> <small>{{.Size}}</small></li>{{else}}<li>No .pkg files found.</li>{{end}}</ul></body></html>`))

func NewContentServer(listen, advertiseURL, root string) *ContentServer {
	return &ContentServer{Listen: listen, AdvertiseURL: strings.TrimRight(advertiseURL, "/"), Root: root, files: make(map[string]servedPackage)}
}

func (s *ContentServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	if s.Listen == "" {
		s.Listen = "0.0.0.0:8081"
	}
	listener, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return fmt.Errorf("listen for PS4 package downloads on %s: %w", s.Listen, err)
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: defaultReadHeaderTimeout()}
	s.listener, s.server = listener, server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (s *ContentServer) Register(pkg Package) ([]string, func(), error) {
	// The listener can run without an advertised URL so operators can verify
	// Docker port publishing independently. An install still needs a LAN URL
	// that the console can reach.
	if err := validateAdvertiseURL(s.AdvertiseURL); err != nil {
		return nil, nil, err
	}
	if err := s.Start(); err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	configuredRoot := s.Root
	s.mu.RUnlock()
	root, err := filepath.Abs(configuredRoot)
	if err != nil {
		return nil, nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve PS4 game directory: %w", err)
	}
	registered := make([]servedPackage, 0, len(pkg.Parts))
	for _, part := range pkg.Parts {
		absolute, err := filepath.Abs(filepath.Clean(part.Path))
		if err != nil {
			return nil, nil, err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve PS4 package part %q: %w", part.Name, err)
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("PS4 package is outside the configured library: %s", part.Name)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, nil, fmt.Errorf("PS4 package part %q: %w", part.Name, err)
		}
		if !info.Mode().IsRegular() || info.Size() != part.Size {
			return nil, nil, fmt.Errorf("PS4 package part changed since scan: %s", part.Name)
		}
		registered = append(registered, servedPackage{path: absolute, name: part.Name})
	}
	tokens := make([]string, 0, len(pkg.Parts))
	urls := make([]string, 0, len(pkg.Parts))
	for index, part := range registered {
		token, err := randomToken()
		if err != nil {
			return nil, nil, err
		}
		s.mu.Lock()
		s.files[token] = part
		s.mu.Unlock()
		tokens = append(tokens, token)
		// RPI runs URLs through the PS4 URI parser before it downloads them.
		// Keep the temporary route ASCII-only and short; the opaque token is the
		// authorization, while ServeContent can retain the real package name.
		urlName := fmt.Sprintf("part-%03d.pkg", index)
		urls = append(urls, s.AdvertiseURL+"/ps4-pkg/"+token+"/"+urlName)
	}
	cleanup := func() {
		s.mu.Lock()
		for _, token := range tokens {
			delete(s.files, token)
		}
		s.mu.Unlock()
	}
	return urls, cleanup, nil
}

func (s *ContentServer) SetRoot(root string) {
	s.mu.Lock()
	s.Root = root
	s.mu.Unlock()
}

func (s *ContentServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("{\"status\":\"ok\",\"service\":\"ps4-package-server\"}\n"))
			}
			return
		}
		if r.URL.Path == "/" {
			s.serveIndex(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ps4-library/") {
			path, name, err := s.resolveLibraryPackage(strings.TrimPrefix(r.URL.EscapedPath(), "/ps4-library/"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			s.servePackage(w, r, servedPackage{path: path, name: name})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ps4-pkg/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		name, err := url.PathUnescape(parts[1])
		if err != nil || !strings.HasSuffix(strings.ToLower(name), ".pkg") {
			http.NotFound(w, r)
			return
		}
		s.mu.RLock()
		served, ok := s.files[parts[0]]
		s.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.servePackage(w, r, served)
	})
}

func (s *ContentServer) servePackage(w http.ResponseWriter, r *http.Request, served servedPackage) {
	file, err := os.Open(served.path)
	if err != nil {
		http.Error(w, "package unavailable", http.StatusGone)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "package unavailable", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, served.name, info.ModTime(), file)
}

func (s *ContentServer) serveIndex(w http.ResponseWriter, r *http.Request) {
	items, err := s.indexedPackages()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err = packageIndexTemplate.Execute(w, items); err != nil {
		return
	}
}

func (s *ContentServer) indexedPackages() ([]indexedPackage, error) {
	s.mu.RLock()
	configuredRoot := s.Root
	s.mu.RUnlock()
	root, err := filepath.Abs(configuredRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("PS4 library unavailable: %w", err)
	}
	items := make([]indexedPackage, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".pkg") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return infoErr
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		items = append(items, indexedPackage{Name: filepath.ToSlash(relative), URL: packageLibraryURL(relative), Size: humanPackageSize(info.Size())})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index PS4 packages: %w", err)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

func (s *ContentServer) resolveLibraryPackage(escapedRelative string) (string, string, error) {
	relativeURL, err := url.PathUnescape(escapedRelative)
	if err != nil || relativeURL == "" {
		return "", "", fmt.Errorf("invalid package path")
	}
	relative := filepath.Clean(filepath.FromSlash(relativeURL))
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !strings.EqualFold(filepath.Ext(relative), ".pkg") {
		return "", "", fmt.Errorf("invalid package path")
	}
	s.mu.RLock()
	configuredRoot := s.Root
	s.mu.RUnlock()
	root, err := filepath.Abs(configuredRoot)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", "", err
	}
	within, err := filepath.Rel(root, path)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("package is outside the configured library")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("package unavailable")
	}
	return path, filepath.Base(path), nil
}

func packageLibraryURL(relative string) string {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return "/ps4-library/" + strings.Join(parts, "/")
}

func humanPackageSize(bytes int64) string {
	const gib = 1 << 30
	const mib = 1 << 20
	if bytes >= gib {
		return fmt.Sprintf("%.2f GiB", float64(bytes)/gib)
	}
	if bytes >= mib {
		return fmt.Sprintf("%.2f MiB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%d bytes", bytes)
}

func (s *ContentServer) Close(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	s.files = make(map[string]servedPackage)
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *ContentServer) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.server != nil
}

func (s *ContentServer) AdvertiseError() error {
	return validateAdvertiseURL(s.AdvertiseURL)
}

func validateAdvertiseURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("PS3MGR_PS4_ADVERTISE_URL is required for PS4 installs (for example http://192.168.1.20:8081)")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("PS3MGR_PS4_ADVERTISE_URL must be an HTTP URL reachable by the PS4")
	}
	host := parsed.Hostname()
	advertisedIP := net.ParseIP(host)
	if strings.EqualFold(host, "localhost") || host == "0.0.0.0" || host == "::" || (advertisedIP != nil && advertisedIP.IsLoopback()) {
		return fmt.Errorf("PS3MGR_PS4_ADVERTISE_URL must use the manager's PS4-reachable LAN address, not %s", host)
	}
	return nil
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func defaultReadHeaderTimeout() time.Duration { return 10 * time.Second }
