package app

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ps3mgr/internal/config"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/events"
	ps3ftp "ps3mgr/internal/ftp"
	"ps3mgr/internal/games"
	"ps3mgr/internal/ps2"
	"ps3mgr/internal/ps4"
	"ps3mgr/internal/ps5"
	"ps3mgr/internal/scanner"
	"ps3mgr/internal/transfers"
)

type Service struct {
	Config    config.Config
	Events    *events.Bus
	Library   *games.Library
	FTP       *ps3ftp.Service
	Scanner   *scanner.Scanner
	Transfers *transfers.Manager
	Pulls     *transfers.Manager
	PS2       *ps2.Service
	PS4       *ps4.Service
	PS5       *ps5.Service

	mu       sync.RWMutex
	local    []domain.Game
	consoles map[string]domain.Console
}

func New(cfg config.Config) *Service {
	bus := events.New()
	ftpService := &ps3ftp.Service{User: cfg.FTPUser, Password: cfg.FTPPassword, Timeout: cfg.FTPTimeout, RemoteRoot: cfg.RemoteGameDir}
	service := &Service{
		Config: cfg, Events: bus, Library: games.NewLibrary(), FTP: ftpService,
		consoles: make(map[string]domain.Console),
	}
	probeTimeout := cfg.ScanTimeout
	if probeTimeout <= 0 {
		probeTimeout = 500 * time.Millisecond
	}
	detectionTimeout := cfg.FTPTimeout
	if detectionTimeout <= 0 {
		detectionTimeout = 8 * time.Second
	}
	service.Scanner = &scanner.Scanner{Detector: ftpService, Workers: cfg.Workers, Timeout: probeTimeout, DetectionTimeout: detectionTimeout}
	service.Transfers = transfers.New(ftpService, bus, cfg.RemoteGameDir)
	service.Pulls = transfers.NewDownload(ftpService, bus, cfg.PS3GameDir, domain.PlatformPS3)
	service.PS2 = ps2.NewService(cfg.PS2GameDir, cfg.PS2SystemDir, cfg.PS2USBRoot, bus)
	service.PS4 = ps4.NewService(cfg.PS4GameDir, cfg.PS4RemoteGameDir, cfg.PS4PKGListen, cfg.PS4AdvertiseURL, cfg.PS4RPIPort, cfg.Workers, cfg.ScanTimeout, cfg.PS4RPITimeout, bus)
	service.PS5 = ps5.NewService(cfg.PS5GameDir, cfg.PS5RemoteGameDir, cfg.PS5FTPUser, cfg.PS5FTPPassword, cfg.PS5FTPPort, cfg.Workers, cfg.ScanTimeout, cfg.FTPTimeout, bus)
	if cfg.PS2CoverDownload {
		service.PS2.Covers = ps2.NewCoverCache()
	}
	return service
}

func (s *Service) LocalGames(ctx context.Context, override string) ([]domain.Game, error) {
	directory := s.Config.PS3GameDir
	if override != "" {
		directory = override
	}
	result, err := s.Library.Scan(ctx, directory)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.local = result
	s.mu.Unlock()
	s.Events.Publish("games.loaded", map[string]any{"source": "local", "count": len(result)})
	return copyGames(result), nil
}

func (s *Service) CachedLocalGames() []domain.Game {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyGames(s.local)
}

func (s *Service) LocalIcon(publicID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, game := range s.local {
		if games.PublicID(game) == publicID && game.IconPath != "" {
			clean := filepath.Clean(game.IconPath)
			root, err1 := filepath.Abs(s.Config.PS3GameDir)
			absolute, err2 := filepath.Abs(clean)
			if err1 == nil && err2 == nil {
				relative, err := filepath.Rel(root, absolute)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return absolute, true
				}
			}
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
	s.Events.Publish("scan.started", map[string]string{"cidr": cidr})
	result, err := scanService.Scan(ctx, cidr, func(console domain.Console) {
		s.mu.Lock()
		s.consoles[console.ID] = console
		s.mu.Unlock()
		s.Events.Publish("scan.host_found", console)
	})
	if err != nil {
		return result, err
	}
	s.Events.Publish("scan.completed", map[string]any{"cidr": cidr, "count": len(result)})
	return result, nil
}

func (s *Service) Consoles() []domain.Console {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Console, 0, len(s.consoles))
	for _, console := range s.consoles {
		result = append(result, console)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IP < result[j].IP })
	return result
}

func (s *Service) Console(id string) (domain.Console, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	console, ok := s.consoles[id]
	return console, ok
}

// AddConsole verifies and registers a directly supplied private/local PS3 IP.
func (s *Service) AddConsole(ctx context.Context, ip string) (domain.Console, error) {
	ip, err := validateConsoleIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	detected, count, err := s.Scanner.Detector.Detect(ctx, ip)
	if err != nil {
		return domain.Console{}, fmt.Errorf("connect to PS3 %s: %w", ip, err)
	}
	if !detected {
		return domain.Console{}, fmt.Errorf("FTP endpoint %s does not appear to be a PS3", ip)
	}
	console := domain.Console{ID: ip, IP: ip, FTPOnline: true, Detected: true, GameCount: count, LastSeen: time.Now()}
	s.mu.Lock()
	s.consoles[ip] = console
	s.mu.Unlock()
	s.Events.Publish("console.connected", console)
	return console, nil
}

func (s *Service) EnsureConsole(ip string) (domain.Console, error) {
	var err error
	ip, err = validateConsoleIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	console := domain.Console{ID: ip, IP: ip}
	s.mu.Lock()
	if known, ok := s.consoles[ip]; ok {
		console = known
	} else {
		s.consoles[ip] = console
	}
	s.mu.Unlock()
	return console, nil
}

func (s *Service) RemoteGames(ctx context.Context, ip, remoteDir string) ([]domain.Game, error) {
	if _, err := s.EnsureConsole(ip); err != nil {
		return nil, err
	}
	if remoteDir == "" {
		remoteDir = s.Config.RemoteGameDir
	}
	result, err := s.FTP.RemoteGames(ctx, ip, remoteDir)
	if err == nil {
		s.mu.Lock()
		console := s.consoles[ip]
		console.ID = ip
		console.IP = ip
		console.FTPOnline = true
		console.Detected = true
		console.GameCount = len(result)
		console.LastSeen = time.Now()
		s.consoles[ip] = console
		s.mu.Unlock()
		s.Events.Publish("games.loaded", map[string]any{"source": "remote", "console_id": ip, "count": len(result)})
		s.Events.Publish("games.updated", map[string]any{"console_id": ip, "count": len(result)})
	}
	return result, err
}

func (s *Service) Compare(ctx context.Context, ip string) ([]domain.Game, error) {
	local := s.CachedLocalGames()
	if local == nil {
		var err error
		local, err = s.LocalGames(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	remote, err := s.RemoteGames(ctx, ip, "")
	if err != nil {
		return nil, err
	}
	return games.Compare(local, remote), nil
}

func (s *Service) Enqueue(consoleIP string, gameIDs []string, stopOnError bool) ([]domain.Transfer, error) {
	if _, err := s.EnsureConsole(consoleIP); err != nil {
		return nil, err
	}
	if len(gameIDs) == 0 {
		return nil, fmt.Errorf("game_ids cannot be empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	selected := make([]domain.Game, 0, len(gameIDs))
	seen := make(map[string]bool)
	for _, wanted := range gameIDs {
		for _, game := range s.local {
			if !seen[wanted] && games.PublicID(game) == wanted {
				selected = append(selected, game)
				seen[wanted] = true
				break
			}
		}
		if !seen[wanted] {
			return nil, fmt.Errorf("unknown local game %q", wanted)
		}
	}
	return s.Transfers.Enqueue(selected, consoleIP, transfers.Options{StopOnError: stopOnError})
}

func (s *Service) EnqueuePull(ctx context.Context, consoleIP string, gameIDs []string, stopOnError bool) ([]domain.Transfer, error) {
	if _, err := s.EnsureConsole(consoleIP); err != nil {
		return nil, err
	}
	remote, err := s.RemoteGames(ctx, consoleIP, "")
	if err != nil {
		return nil, err
	}
	if len(gameIDs) == 0 {
		return nil, fmt.Errorf("game_ids cannot be empty")
	}
	selected := make([]domain.Game, 0, len(gameIDs))
	for _, wanted := range gameIDs {
		found := false
		for _, game := range remote {
			if games.PublicID(game) == wanted {
				selected = append(selected, game)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown remote game %q", wanted)
		}
	}
	return s.Pulls.Enqueue(selected, consoleIP, transfers.Options{StopOnError: stopOnError})
}

func (s *Service) Close(ctx context.Context) error {
	errors := make(chan error, 5)
	go func() { errors <- s.PS2.Close(ctx) }()
	go func() { errors <- s.Transfers.Close(ctx) }()
	go func() {
		if s.Pulls == nil {
			errors <- nil
			return
		}
		errors <- s.Pulls.Close(ctx)
	}()
	go func() { errors <- s.PS4.Close(ctx) }()
	go func() { errors <- s.PS5.Close(ctx) }()
	var first error
	for range 5 {
		if err := <-errors; err != nil && first == nil {
			first = err
		}
	}
	return first
}

func copyGames(value []domain.Game) []domain.Game {
	if value == nil {
		return nil
	}
	result := make([]domain.Game, len(value))
	copy(result, value)
	return result
}

func validateConsoleIP(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed := net.ParseIP(value)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("invalid IPv4 address")
	}
	if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsLinkLocalUnicast() {
		return "", fmt.Errorf("PS3 address must be private or local")
	}
	return parsed.String(), nil
}
