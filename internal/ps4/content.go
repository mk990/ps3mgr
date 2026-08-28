package ps4

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	if err := validateAdvertiseURL(s.AdvertiseURL); err != nil {
		return err
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
	for _, part := range registered {
		token, err := randomToken()
		if err != nil {
			return nil, nil, err
		}
		s.mu.Lock()
		s.files[token] = part
		s.mu.Unlock()
		tokens = append(tokens, token)
		urls = append(urls, s.AdvertiseURL+"/ps4-pkg/"+token+"/"+url.PathEscape(part.name))
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
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ps4-pkg/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		name, err := url.PathUnescape(parts[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.mu.RLock()
		served, ok := s.files[parts[0]]
		s.mu.RUnlock()
		if !ok || name != served.name {
			http.NotFound(w, r)
			return
		}
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
	})
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
