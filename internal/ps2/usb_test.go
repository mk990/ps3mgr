package ps2

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUSBDiscoveryAndRemoval(t *testing.T) {
	root := t.TempDir()
	usb := filepath.Join(root, "usb0")
	if err := os.Mkdir(usb, 0755); err != nil {
		t.Fatal(err)
	}
	manager := NewUSBManager(root, nil)
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	target, ok := manager.Get("usb0")
	if !ok || !target.Available || target.FreeBytes <= 0 {
		t.Fatalf("target = %#v, ok=%v", target, ok)
	}
	if discovery := manager.Discovery(); discovery.Mode != "children" {
		t.Fatalf("mode = %s", discovery.Mode)
	}
	if err := os.Remove(usb); err != nil {
		t.Fatal(err)
	}
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("usb0"); ok {
		t.Fatal("removed USB is still registered")
	}
}

func TestUSBDiscoverySupportsDirectRootMount(t *testing.T) {
	root := t.TempDir()
	manager := NewUSBManager(root, nil)
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	target, ok := manager.Get("usb-root")
	if !ok || target.MountPath != root {
		t.Fatalf("target = %#v, ok=%v", target, ok)
	}
	if discovery := manager.Discovery(); discovery.Mode != "direct-root" || len(discovery.Targets) != 1 {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestDirectRootOPLDirectoriesAreNotDevices(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"DVD", "ART", "CFG"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewUSBManager(root, nil)
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	discovery := manager.Discovery()
	if discovery.Mode != "direct-root" || len(discovery.Targets) != 1 || discovery.Targets[0].ID != "usb-root" {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestMissingUSBRootReportsDiagnostic(t *testing.T) {
	manager := NewUSBManager(filepath.Join(t.TempDir(), "missing"), nil)
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	discovery := manager.Discovery()
	if discovery.Mode != "unavailable" || len(discovery.Issues) != 1 || len(discovery.Targets) != 0 {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestUSBIDsCannotEscapeMountRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	manager := NewUSBManager(root, nil)
	if err := manager.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("escape"); ok {
		t.Fatal("outside symlink was registered")
	}
	if _, err := manager.Validate("../etc", 0); err == nil {
		t.Fatal("path-like target ID was accepted")
	}
}

func TestUSBInsufficientSpace(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "usb0"), 0755); err != nil {
		t.Fatal(err)
	}
	manager := NewUSBManager(root, nil)
	if _, err := manager.Validate("usb0", int64(^uint64(0)>>1)); err == nil {
		t.Fatal("insufficient space was accepted")
	}
}

func TestFAT32CompatibilityClassification(t *testing.T) {
	tests := []struct {
		filesystem, status string
		compatible         bool
	}{{"vfat", "COMPATIBLE", true}, {"fat32", "COMPATIBLE", true}, {"exfat", "INCOMPATIBLE", false}, {"ext4", "INCOMPATIBLE", false}, {"fuse", "UNKNOWN", false}, {"", "UNKNOWN", false}}
	for _, test := range tests {
		t.Run(test.filesystem, func(t *testing.T) {
			compatible, status, note := classifyFAT32Compatibility(test.filesystem)
			if compatible != test.compatible || status != test.status || note == "" {
				t.Fatalf("got compatible=%v status=%s note=%q", compatible, status, note)
			}
		})
	}
}
