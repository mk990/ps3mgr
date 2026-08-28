package ps4

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/scanner"
)

// Service owns PS4 library, Remote Package Installer console, content server,
// and queue state. It is independent from every FTP and OPL worker.
type Service struct {
	GameDir string
	Library Library
	RPI     *RPIClient
	Content *ContentServer
	Scanner *scanner.Scanner
	Queue   *Queue
	Covers  *CoverCache
	events  Publisher

	mu       sync.RWMutex
	local    []Package
	consoles map[string]domain.Console
}

func NewService(gameDir, listen, advertiseURL string, rpiPort, workers int, scanTimeout, requestTimeout time.Duration, events Publisher) *Service {
	if gameDir == "" {
		gameDir = "./ps4-games"
	}
	if rpiPort == 0 {
		rpiPort = DefaultRPIPort
	}
	if scanTimeout <= 0 {
		scanTimeout = 500 * time.Millisecond
	}
	client := NewRPIClient(rpiPort, requestTimeout)
	content := NewContentServer(listen, advertiseURL, gameDir)
	covers := &CoverCache{}
	return &Service{
		GameDir: gameDir, RPI: client, Content: content, Covers: covers, events: events,
		Scanner:  &scanner.Scanner{Detector: client, Workers: workers, Timeout: scanTimeout, DetectionTimeout: requestTimeout, Port: fmt.Sprint(rpiPort)},
		Queue:    NewQueue(client, content, events),
		consoles: make(map[string]domain.Console),
	}
}

func (s *Service) LocalPackages(ctx context.Context, override string) ([]Package, error) {
	root := s.GameDir
	if override != "" {
		root = override
	}
	items, err := s.Library.Scan(ctx, root)
	if err != nil {
		return nil, err
	}
	extracted, failures := s.Covers.Populate(ctx, root, items)
	if extracted > 0 {
		s.publish("ps4.covers.cached", map[string]any{"platform": Platform, "extracted": extracted, "directory": filepath.Join(root, "covers")})
	}
	if len(failures) > 0 {
		s.publish("ps4.covers.failed", map[string]any{"platform": Platform, "failures": failures})
	}
	s.Content.SetRoot(root)
	s.mu.Lock()
	s.local = items
	s.mu.Unlock()
	s.publish("ps4.games.loaded", map[string]any{"platform": Platform, "count": len(items)})
	return copyPackages(items), nil
}

func (s *Service) CachedPackages() []Package {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyPackages(s.local)
}

func (s *Service) CoverStatus() CoverStatus { return s.Covers.Status(s.GameDir) }

func (s *Service) Cover(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pkg := range s.local {
		if pkg.ID != id || pkg.CoverPath == "" {
			continue
		}
		root, rootErr := filepath.EvalSymlinks(s.GameDir)
		cover, coverErr := filepath.EvalSymlinks(pkg.CoverPath)
		if rootErr != nil || coverErr != nil {
			return "", false
		}
		root, rootErr = filepath.Abs(root)
		cover, coverErr = filepath.Abs(cover)
		if rootErr != nil || coverErr != nil {
			return "", false
		}
		relative, err := filepath.Rel(root, cover)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return cover, true
		}
	}
	return "", false
}

func (s *Service) Scan(ctx context.Context, cidr string, workers int) ([]domain.Console, error) {
	scanService := *s.Scanner
	if workers > 0 {
		if workers > 256 {
			return nil, fmt.Errorf("workers must be between 1 and 256")
		}
		scanService.Workers = workers
	}
	s.publish("ps4.scan.started", map[string]any{"platform": Platform, "cidr": cidr, "api_port": s.RPI.Port})
	result, err := scanService.Scan(ctx, cidr, func(console domain.Console) {
		console.Platform, console.APIPort = domain.PlatformPS4, s.RPI.Port
		console.FTPOnline = false
		s.mu.Lock()
		s.consoles[console.ID] = console
		s.mu.Unlock()
		s.publish("ps4.scan.host_found", console)
	})
	for i := range result {
		result[i].Platform, result[i].APIPort, result[i].FTPOnline = domain.PlatformPS4, s.RPI.Port, false
	}
	if err == nil {
		s.publish("ps4.scan.completed", map[string]any{"platform": Platform, "cidr": cidr, "count": len(result)})
	}
	return result, err
}

func (s *Service) AddConsole(ctx context.Context, ip string) (domain.Console, error) {
	ip, err := validateIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	detected, _, err := s.RPI.Detect(ctx, ip)
	if err != nil {
		return domain.Console{}, fmt.Errorf("connect to PS4 Remote Package Installer %s:%d: %w", ip, s.RPI.Port, err)
	}
	if !detected {
		return domain.Console{}, fmt.Errorf("%s:%d is not a PS4 Remote Package Installer", ip, s.RPI.Port)
	}
	console := domain.Console{ID: ip, IP: ip, Platform: domain.PlatformPS4, APIPort: s.RPI.Port, Detected: true, LastSeen: time.Now()}
	s.mu.Lock()
	s.consoles[ip] = console
	s.mu.Unlock()
	s.publish("ps4.console.connected", console)
	return console, nil
}

func (s *Service) EnsureConsole(ip string) (domain.Console, error) {
	ip, err := validateIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if console, ok := s.consoles[ip]; ok {
		return console, nil
	}
	console := domain.Console{ID: ip, IP: ip, Platform: domain.PlatformPS4, APIPort: s.RPI.Port}
	s.consoles[ip] = console
	return console, nil
}

func (s *Service) Consoles() []domain.Console {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Console, 0, len(s.consoles))
	for _, console := range s.consoles {
		items = append(items, console)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].IP < items[j].IP })
	return items
}

func (s *Service) Console(id string) (domain.Console, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	console, ok := s.consoles[id]
	return console, ok
}

func (s *Service) Compare(ctx context.Context, ip string) ([]Package, error) {
	console, err := s.EnsureConsole(ip)
	if err != nil {
		return nil, err
	}
	items := s.CachedPackages()
	if items == nil {
		items, err = s.LocalPackages(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	installedByTitle := make(map[string]bool)
	for i := range items {
		// RPI's is_exists endpoint reports whether the base title exists. It
		// cannot prove that a particular patch, DLC, or license is installed.
		if items[i].TitleID == "" || items[i].Format != "pkg-game" {
			continue
		}
		installed, ok := installedByTitle[items[i].TitleID]
		if !ok {
			installed, err = s.RPI.IsInstalled(ctx, ip, items[i].TitleID)
			if err != nil {
				return nil, fmt.Errorf("check %s on PS4 %s: %w", items[i].TitleID, ip, err)
			}
			installedByTitle[items[i].TitleID] = installed
		}
		items[i].Installed = installed
	}
	count := 0
	for _, installed := range installedByTitle {
		if installed {
			count++
		}
	}
	console.Detected, console.GameCount, console.LastSeen = true, count, time.Now()
	s.mu.Lock()
	s.consoles[ip] = console
	s.mu.Unlock()
	s.publish("ps4.games.compared", map[string]any{"platform": Platform, "console_id": ip, "count": len(items)})
	return items, nil
}

func (s *Service) Enqueue(consoleIP string, packageIDs []string, stopOnError bool) ([]Job, error) {
	if _, err := s.EnsureConsole(consoleIP); err != nil {
		return nil, err
	}
	if len(packageIDs) == 0 {
		return nil, fmt.Errorf("package_ids cannot be empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	selected := make([]Package, 0, len(packageIDs))
	seen := make(map[string]bool)
	for _, wanted := range packageIDs {
		for _, pkg := range s.local {
			if !seen[wanted] && pkg.ID == wanted {
				selected = append(selected, pkg)
				seen[wanted] = true
				break
			}
		}
		if !seen[wanted] {
			return nil, fmt.Errorf("unknown local PS4 package %q", wanted)
		}
	}
	return s.Queue.Enqueue(selected, consoleIP, stopOnError)
}

func (s *Service) ContentStatus() map[string]any {
	return map[string]any{"listen": s.Content.Listen, "advertise_url": s.Content.AdvertiseURL, "configured": s.Content.AdvertiseURL != "", "running": s.Content.Running(), "rpi_port": s.RPI.Port}
}

func (s *Service) Close(ctx context.Context) error {
	if err := s.Queue.Close(ctx); err != nil {
		return err
	}
	return s.Content.Close(ctx)
}

func (s *Service) publish(event string, payload any) {
	if s.events != nil {
		s.events.Publish(event, payload)
	}
}

func validateIP(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed := net.ParseIP(value)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("invalid IPv4 address")
	}
	if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsLinkLocalUnicast() {
		return "", fmt.Errorf("PS4 address must be private or local")
	}
	return parsed.String(), nil
}

func copyPackages(items []Package) []Package {
	if items == nil {
		return nil
	}
	result := make([]Package, len(items))
	for i := range items {
		result[i] = items[i]
		result[i].Parts = append([]PackagePart(nil), items[i].Parts...)
	}
	return result
}
