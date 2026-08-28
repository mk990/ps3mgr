package ps2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrUSBUnavailable = errors.New("PS2 USB target unavailable")

type Publisher interface{ Publish(string, any) }

type USBManager struct {
	root    string
	events  Publisher
	mu      sync.RWMutex
	targets map[string]USBTarget
	issues  []USBScanIssue
	mode    string
	stop    context.CancelFunc
	done    chan struct{}
}

func NewUSBManager(root string, events Publisher) *USBManager {
	if root == "" {
		root = "/mnt/usb"
	}
	absolute, err := filepath.Abs(root)
	if err == nil {
		root = absolute
	}
	return &USBManager{root: filepath.Clean(root), events: events, targets: make(map[string]USBTarget)}
}

func (m *USBManager) Start(ctx context.Context, interval time.Duration) {
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	m.stop = cancel
	m.done = make(chan struct{})
	m.mu.Unlock()
	if interval <= 0 {
		interval = 3 * time.Second
	}
	go func() {
		defer close(m.done)
		_ = m.Refresh()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				_ = m.Refresh()
			}
		}
	}()
}

func (m *USBManager) Close() {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (m *USBManager) Refresh() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			m.replaceTargets(nil, []USBScanIssue{{Path: m.root, Reason: "configured USB mount root does not exist"}}, "unavailable")
			return nil
		} else {
			return fmt.Errorf("scan PS2 USB mount root %q: %w", m.root, err)
		}
	}
	type candidate struct{ id, name, path string }
	issues := make([]USBScanIssue, 0)
	candidates := make([]candidate, 0)
	rootHasOPL := hasOPLMarker(m.root)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || isOPLRootDirectory(entry.Name()) {
			continue
		}
		path := filepath.Join(m.root, entry.Name())
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			issues = append(issues, USBScanIssue{Path: path, Reason: resolveErr.Error()})
			continue
		}
		rel, relErr := filepath.Rel(m.root, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			issues = append(issues, USBScanIssue{Path: path, Reason: "resolved path escapes the configured USB mount root"})
			continue
		}
		candidates = append(candidates, candidate{id: entry.Name(), name: strings.ToUpper(entry.Name()), path: path})
	}
	mode := "children"
	if rootHasOPL || len(candidates) == 0 {
		mode = "direct-root"
		candidates = []candidate{{id: "usb-root", name: "USB ROOT", path: m.root}}
	}
	next := make(map[string]USBTarget)
	for _, item := range candidates {
		total, free, readOnly, filesystem, err := filesystemCapacity(item.path)
		if err != nil {
			issues = append(issues, USBScanIssue{Path: item.path, Reason: err.Error()})
			continue
		}
		compatible, status, note := classifyFAT32Compatibility(filesystem)
		target := USBTarget{ID: item.id, Name: item.name, MountPath: item.path, Filesystem: filesystem, FAT32Compatible: compatible, FAT32Status: status, CompatibilityNote: note, TotalBytes: total, UsedBytes: total - free, FreeBytes: free, ReadOnly: readOnly, Available: true}
		_, dvdErr := os.Stat(filepath.Join(item.path, "DVD"))
		_, cfgErr := os.Stat(filepath.Join(item.path, "ul.cfg"))
		target.OPLReady = dvdErr == nil || cfgErr == nil
		next[target.ID] = target
	}
	m.replaceTargets(next, issues, mode)
	return nil
}

func (m *USBManager) replaceTargets(next map[string]USBTarget, issues []USBScanIssue, mode string) {
	if next == nil {
		next = make(map[string]USBTarget)
	}
	m.mu.Lock()
	previous := m.targets
	previousIssues := append([]USBScanIssue(nil), m.issues...)
	m.targets = next
	m.issues = make([]USBScanIssue, len(issues))
	copy(m.issues, issues)
	m.mode = mode
	m.mu.Unlock()
	if m.events != nil {
		for id, target := range next {
			if _, ok := previous[id]; !ok {
				m.events.Publish("ps2.usb.connected", map[string]any{"platform": Platform, "usb": target})
			}
		}
		for id, target := range previous {
			if _, ok := next[id]; !ok {
				target.Available = false
				m.events.Publish("ps2.usb.disconnected", map[string]any{"platform": Platform, "usb": target})
			}
		}
		knownIssues := make(map[string]bool, len(previousIssues))
		for _, issue := range previousIssues {
			knownIssues[issue.Path+"\x00"+issue.Reason] = true
		}
		for _, issue := range issues {
			if !knownIssues[issue.Path+"\x00"+issue.Reason] {
				m.events.Publish("ps2.usb.skipped", map[string]any{"platform": Platform, "path": issue.Path, "reason": issue.Reason})
			}
		}
	}
}

func hasOPLMarker(path string) bool {
	for _, name := range []string{"DVD", "CD", "ul.cfg"} {
		if _, err := os.Stat(filepath.Join(path, name)); err == nil {
			return true
		}
	}
	return false
}
func isOPLRootDirectory(name string) bool {
	switch strings.ToUpper(name) {
	case "DVD", "CD", "ART", "CFG", "VMC", "THM", "APPS", "SYSTEM VOLUME INFORMATION", "LOST.DIR", "RECYCLE.BIN":
		return true
	}
	return false
}

func (m *USBManager) List() []USBTarget {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]USBTarget, 0, len(m.targets))
	for _, target := range m.targets {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *USBManager) Discovery() USBDiscovery {
	m.mu.RLock()
	defer m.mu.RUnlock()
	targets := make([]USBTarget, 0, len(m.targets))
	for _, target := range m.targets {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	issues := make([]USBScanIssue, len(m.issues))
	copy(issues, m.issues)
	return USBDiscovery{Root: m.root, Mode: m.mode, Targets: targets, Issues: issues}
}

func (m *USBManager) Get(id string) (USBTarget, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.targets[id]
	return value, ok
}

func (m *USBManager) Validate(id string, required int64) (USBTarget, error) {
	if err := m.Refresh(); err != nil {
		return USBTarget{}, err
	}
	target, ok := m.Get(id)
	if !ok || !target.Available {
		return USBTarget{}, fmt.Errorf("%w: %s", ErrUSBUnavailable, id)
	}
	if target.ReadOnly {
		return USBTarget{}, fmt.Errorf("PS2 USB target is read-only: %s", id)
	}
	if required > target.FreeBytes {
		return USBTarget{}, fmt.Errorf("insufficient space on %s: required %s, available %s", target.Name, formatBytes(required), formatBytes(target.FreeBytes))
	}
	return target, nil
}

func formatBytes(value int64) string {
	const gib = 1 << 30
	return fmt.Sprintf("%.2f GiB", float64(value)/gib)
}

func classifyFAT32Compatibility(filesystem string) (bool, string, string) {
	switch strings.ToLower(strings.TrimSpace(filesystem)) {
	case "vfat", "msdos", "fat", "fat32":
		return true, "COMPATIBLE", "FAT-family filesystem detected; mounted metadata cannot distinguish FAT16 from FAT32."
	case "exfat":
		return false, "INCOMPATIBLE", "exFAT is not FAT32; verify support in the installed OPL version."
	case "ntfs", "ext2", "ext3", "ext4", "xfs", "btrfs", "tmpfs":
		return false, "INCOMPATIBLE", "Filesystem is not FAT32. Format the device as MBR/FAT32 on the Docker host if required."
	default:
		return false, "UNKNOWN", "The container cannot confirm FAT32 from this mounted directory; verify the filesystem on the Docker host."
	}
}
