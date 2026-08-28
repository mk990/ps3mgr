package ps2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Service struct {
	GameDir    string
	SystemDir  string
	Library    Library
	USB        *USBManager
	Queue      *Queue
	Filesystem *Filesystem
	Covers     *CoverCache
	events     Publisher
	scanMu     sync.Mutex
	mu         sync.RWMutex
	games      []Game
}

type installer struct {
	usb        *USBManager
	filesystem *Filesystem
}

func (i installer) Process(ctx context.Context, game Game, usbID string, stage func(State), progress ProgressFunc) error {
	stage(StatePreparing)
	required, err := i.filesystem.RequiredBytes()
	if err != nil {
		return err
	}
	target, err := i.usb.Validate(usbID, game.Size+required+64)
	if err != nil {
		return err
	}
	if !i.filesystem.useDirectCopy(game.Size) {
		stage(StateConverting)
	} else {
		stage(StateWriting)
	}
	result, err := i.filesystem.InstallGame(ctx, game, target, progress)
	if err != nil {
		_ = i.usb.Refresh()
		if _, available := i.usb.Get(usbID); !available {
			return fmt.Errorf("%w: %s", ErrUSBUnavailable, usbID)
		}
		return fmt.Errorf("OPL preparation failed for %s: %w", game.Title, err)
	}
	stage(StateVerifying)
	if err = i.filesystem.Verify(ctx, result); err != nil {
		return err
	}
	if _, ok := i.usb.Get(usbID); !ok {
		return fmt.Errorf("%w before verification completed: %s", ErrUSBUnavailable, usbID)
	}
	return nil
}

func NewService(gameDir, systemDir, usbRoot string, events Publisher) *Service {
	if gameDir == "" {
		gameDir = "./ps2-games"
	}
	if systemDir == "" {
		systemDir = "./ps2-system"
	}
	filesystem := &Filesystem{SystemDir: systemDir}
	usb := NewUSBManager(usbRoot, events)
	s := &Service{GameDir: gameDir, SystemDir: systemDir, Library: Library{}, USB: usb, Filesystem: filesystem, events: events}
	s.Queue = NewQueue(installer{usb: usb, filesystem: filesystem}, events)
	usb.Start(context.Background(), 3*time.Second)
	return s
}

func (s *Service) LocalGames(ctx context.Context, override string) ([]Game, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	directory := s.GameDir
	if override != "" {
		directory = override
	}
	s.publish("ps2.scan.started", map[string]any{"platform": Platform, "directory": directory})
	items, err := s.Library.Scan(ctx, directory)
	if err != nil {
		return nil, err
	}
	if systemRoot, rootErr := filepath.Abs(s.SystemDir); rootErr == nil {
		filtered := items[:0]
		for _, game := range items {
			isoPath, pathErr := filepath.Abs(game.ISOPath)
			relative, relErr := filepath.Rel(systemRoot, isoPath)
			if pathErr == nil && relErr == nil && (relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..")) {
				continue
			}
			filtered = append(filtered, game)
		}
		items = filtered
	}
	if s.Covers != nil {
		downloaded, failures := s.Covers.Populate(ctx, directory, items)
		if downloaded > 0 {
			s.publish("ps2.covers.cached", map[string]any{"platform": Platform, "downloaded": downloaded, "directory": filepath.Join(directory, "covers")})
		}
		if len(failures) > 0 {
			s.publish("ps2.covers.failed", map[string]any{"platform": Platform, "failures": failures})
		}
	}
	s.mu.Lock()
	s.games = items
	s.mu.Unlock()
	s.publish("ps2.games.loaded", map[string]any{"platform": Platform, "count": len(items)})
	return copyGames(items), nil
}
func (s *Service) CachedGames() []Game {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyGames(s.games)
}
func (s *Service) Game(id string) (Game, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, game := range s.games {
		if game.PublicID == id {
			return game, true
		}
	}
	return Game{}, false
}
func (s *Service) Cover(id string) (string, bool) {
	game, ok := s.Game(id)
	if !ok || game.CoverPath == "" {
		return "", false
	}
	root, err := filepath.EvalSymlinks(s.GameDir)
	if err != nil {
		return "", false
	}
	cover, err := filepath.EvalSymlinks(game.CoverPath)
	if err != nil {
		return "", false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", false
	}
	cover, err = filepath.Abs(cover)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, cover)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cover, true
}
func (s *Service) USBTargets() ([]USBTarget, error) {
	if err := s.USB.Refresh(); err != nil {
		return nil, err
	}
	return s.USB.List(), nil
}

func (s *Service) USBDiscovery() (USBDiscovery, error) {
	if err := s.USB.Refresh(); err != nil {
		return USBDiscovery{}, err
	}
	return s.USB.Discovery(), nil
}

func (s *Service) PrepareUSB(ctx context.Context, usbID string) (USBTarget, error) {
	target, err := s.USB.Validate(usbID, 0)
	if err != nil {
		return USBTarget{}, err
	}
	if err = s.Filesystem.Prepare(ctx, target); err != nil {
		return USBTarget{}, fmt.Errorf("initialize OPL layout on %s: %w", target.Name, err)
	}
	if err = s.USB.Refresh(); err != nil {
		return USBTarget{}, err
	}
	updated, ok := s.USB.Get(usbID)
	if !ok {
		return USBTarget{}, fmt.Errorf("%w after OPL initialization: %s", ErrUSBUnavailable, usbID)
	}
	s.publish("ps2.usb.prepared", map[string]any{"platform": Platform, "usb": updated})
	return updated, nil
}

func (s *Service) Compare(usbID string) ([]CompareResult, error) {
	target, ok := s.USB.Get(usbID)
	if !ok {
		return nil, fmt.Errorf("PS2 USB target %q not found", usbID)
	}
	games := s.CachedGames()
	if games == nil {
		return nil, fmt.Errorf("PS2 local library has not been scanned")
	}
	cfg, _ := os.ReadFile(filepath.Join(target.MountPath, "ul.cfg"))
	out := make([]CompareResult, 0, len(games))
	for _, game := range games {
		serial := oplGameID(game.ID)
		installed := strings.Contains(string(cfg), "ul."+serial)
		if !installed {
			matches, _ := filepath.Glob(filepath.Join(target.MountPath, "DVD", serial+".*.iso"))
			installed = len(matches) > 0
		}
		game.USBInstalled = installed
		out = append(out, CompareResult{Game: game, Installed: installed})
	}
	return out, nil
}

func (s *Service) Enqueue(usbID string, gameIDs []string) ([]Job, error) {
	if _, err := s.USB.Validate(usbID, 0); err != nil {
		return nil, err
	}
	if len(gameIDs) == 0 {
		return nil, fmt.Errorf("game_ids cannot be empty")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	selected := make([]Game, 0, len(gameIDs))
	for _, wanted := range gameIDs {
		found := false
		for _, game := range s.games {
			if game.PublicID == wanted {
				selected = append(selected, game)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown PS2 game %q", wanted)
		}
	}
	for _, game := range selected {
		if game.ID == "unknown" {
			return nil, fmt.Errorf("PS2 game %q has an unknown game ID and cannot be prepared for OPL", game.Title)
		}
	}
	return s.Queue.Enqueue(selected, usbID)
}
func (s *Service) Close(ctx context.Context) error { s.USB.Close(); return s.Queue.Close(ctx) }
func (s *Service) publish(event string, payload any) {
	if s.events != nil {
		s.events.Publish(event, payload)
	}
}

func copyGames(value []Game) []Game {
	if value == nil {
		return nil
	}
	result := make([]Game, len(value))
	copy(result, value)
	return result
}
