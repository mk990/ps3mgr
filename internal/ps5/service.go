package ps5

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
	"ps3mgr/internal/games"
	"ps3mgr/internal/scanner"
	"ps3mgr/internal/transfers"
)

type Publisher interface {
	Publish(eventType string, payload any)
}

// Service owns all PS5/ShadowMountPlus state. Its queue is deliberately
// independent from both the PS2 and PS3 workers.
type Service struct {
	GameDir   string
	RemoteDir string
	Library   Library
	FTP       *FTP
	Scanner   *scanner.Scanner
	Transfers *transfers.Manager
	Pulls     *transfers.Manager
	events    Publisher

	mu       sync.RWMutex
	local    []domain.Game
	consoles map[string]domain.Console
}

func NewService(gameDir, remoteDir, user, password string, port, workers int, scanTimeout, ftpTimeout time.Duration, events Publisher) *Service {
	if gameDir == "" {
		gameDir = "./ps5-games"
	}
	if remoteDir == "" {
		remoteDir = "/data/etaHEN/games"
	}
	if port == 0 {
		port = DefaultFTPPort
	}
	if scanTimeout <= 0 {
		scanTimeout = 500 * time.Millisecond
	}
	if ftpTimeout <= 0 {
		ftpTimeout = 8 * time.Second
	}
	ftpService := NewFTP(user, password, ftpTimeout, port, remoteDir)
	return &Service{
		GameDir: gameDir, RemoteDir: remoteDir, FTP: ftpService, events: events,
		Scanner:   &scanner.Scanner{Detector: ftpService, Workers: workers, Timeout: scanTimeout, DetectionTimeout: ftpTimeout, Port: fmt.Sprint(port)},
		Transfers: transfers.NewPlatform(ftpService, events, remoteDir, domain.PlatformPS5),
		Pulls:     transfers.NewDownload(ftpService, events, gameDir, domain.PlatformPS5),
		consoles:  make(map[string]domain.Console),
	}
}

func (s *Service) LocalGames(ctx context.Context, override string) ([]domain.Game, error) {
	root := s.GameDir
	if override != "" {
		root = override
	}
	result, err := s.Library.Scan(ctx, root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.local = result
	s.mu.Unlock()
	s.publish("ps5.games.loaded", map[string]any{"platform": domain.PlatformPS5, "count": len(result)})
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
	root, err := filepath.Abs(s.GameDir)
	if err != nil {
		return "", false
	}
	for _, game := range s.local {
		if games.PublicID(game) != publicID || game.IconPath == "" {
			continue
		}
		absolute, err := filepath.Abs(filepath.Clean(game.IconPath))
		if err != nil {
			return "", false
		}
		relative, err := filepath.Rel(root, absolute)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return absolute, true
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
	s.publish("ps5.scan.started", map[string]any{"platform": domain.PlatformPS5, "cidr": cidr, "ftp_port": s.FTP.Port})
	result, err := scanService.Scan(ctx, cidr, func(console domain.Console) {
		console.Platform = domain.PlatformPS5
		console.FTPPort = s.FTP.Port
		s.mu.Lock()
		s.consoles[console.ID] = console
		s.mu.Unlock()
		s.publish("ps5.scan.host_found", console)
	})
	for i := range result {
		result[i].Platform = domain.PlatformPS5
		result[i].FTPPort = s.FTP.Port
	}
	if err != nil {
		return result, err
	}
	s.publish("ps5.scan.completed", map[string]any{"platform": domain.PlatformPS5, "cidr": cidr, "count": len(result)})
	return result, nil
}

func (s *Service) AddConsole(ctx context.Context, ip string) (domain.Console, error) {
	ip, err := validateIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	detected, count, err := s.FTP.Detect(ctx, ip)
	if err != nil {
		return domain.Console{}, fmt.Errorf("connect to PS5 %s:%d: %w", ip, s.FTP.Port, err)
	}
	if !detected {
		return domain.Console{}, fmt.Errorf("FTP endpoint %s:%d does not expose etaHEN data", ip, s.FTP.Port)
	}
	console := domain.Console{ID: ip, IP: ip, Platform: domain.PlatformPS5, FTPPort: s.FTP.Port, FTPOnline: true, Detected: true, GameCount: count, LastSeen: time.Now()}
	s.mu.Lock()
	s.consoles[ip] = console
	s.mu.Unlock()
	s.publish("ps5.console.connected", console)
	return console, nil
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

func (s *Service) EnsureConsole(ip string) (domain.Console, error) {
	ip, err := validateIP(ip)
	if err != nil {
		return domain.Console{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if known, ok := s.consoles[ip]; ok {
		return known, nil
	}
	console := domain.Console{ID: ip, IP: ip, Platform: domain.PlatformPS5, FTPPort: s.FTP.Port}
	s.consoles[ip] = console
	return console, nil
}

func (s *Service) RemoteGames(ctx context.Context, ip string) ([]domain.Game, error) {
	if _, err := s.EnsureConsole(ip); err != nil {
		return nil, err
	}
	result, err := s.FTP.RemoteGames(ctx, ip)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	console := s.consoles[ip]
	console.FTPOnline, console.Detected, console.GameCount, console.LastSeen = true, true, len(result), time.Now()
	s.consoles[ip] = console
	s.mu.Unlock()
	s.publish("ps5.games.loaded", map[string]any{"platform": domain.PlatformPS5, "source": "remote", "console_id": ip, "count": len(result)})
	return result, nil
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
	remote, err := s.RemoteGames(ctx, ip)
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
			return nil, fmt.Errorf("unknown local PS5 game %q", wanted)
		}
	}
	return s.Transfers.Enqueue(selected, consoleIP, transfers.Options{StopOnError: stopOnError})
}

func (s *Service) EnqueuePull(ctx context.Context, consoleIP string, gameIDs []string, stopOnError bool) ([]domain.Transfer, error) {
	if _, err := s.EnsureConsole(consoleIP); err != nil {
		return nil, err
	}
	remote, err := s.RemoteGames(ctx, consoleIP)
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
			if games.PublicID(game) == wanted || strings.EqualFold(game.Title, wanted) || strings.EqualFold(game.ID, wanted) {
				selected = append(selected, game)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown remote PS5 game %q", wanted)
		}
	}
	return s.Pulls.Enqueue(selected, consoleIP, transfers.Options{StopOnError: stopOnError})
}

func (s *Service) Close(ctx context.Context) error {
	if err := s.Transfers.Close(ctx); err != nil {
		return err
	}
	return s.Pulls.Close(ctx)
}

func (s *Service) publish(eventType string, payload any) {
	if s.events != nil {
		s.events.Publish(eventType, payload)
	}
}

func validateIP(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed := net.ParseIP(value)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("invalid IPv4 address")
	}
	if !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsLinkLocalUnicast() {
		return "", fmt.Errorf("PS5 address must be private or local")
	}
	return parsed.String(), nil
}

func copyGames(value []domain.Game) []domain.Game {
	if value == nil {
		return nil
	}
	result := make([]domain.Game, len(value))
	copy(result, value)
	return result
}
