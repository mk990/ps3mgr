package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type ReadinessCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Path     string `json:"path,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type ReadinessReport struct {
	Ready  bool             `json:"ready"`
	Status string           `json:"status"`
	Checks []ReadinessCheck `json:"checks"`
}

// Readiness reports configuration and filesystem problems without probing a
// console. Required library failures return Ready=false; platform-specific
// installation helpers remain visible as warnings so an unused platform does
// not make the whole panel unavailable.
func (s *Service) Readiness() ReadinessReport {
	checks := []ReadinessCheck{
		directoryCheck("ps3_library", s.Config.PS3GameDir, true),
		directoryCheck("ps2_library", s.PS2.GameDir, true),
		directoryCheck("ps4_library", s.PS4.GameDir, true),
		directoryCheck("ps5_library", s.PS5.GameDir, true),
		directoryCheck("ps2_system", s.PS2.SystemDir, false),
		directoryCheck("ps2_usb_root", s.Config.PS2USBRoot, false),
	}

	ps2Cache := filepath.Join(s.PS2.GameDir, "covers")
	if s.PS2.Covers == nil {
		checks = append(checks, ReadinessCheck{Name: "ps2_cover_cache", Status: "ok", Path: ps2Cache, Detail: "cover downloads are disabled"})
	} else {
		checks = append(checks, writableDirectoryCheck("ps2_cover_cache", ps2Cache))
	}
	checks = append(checks, writableDirectoryCheck("ps4_cover_cache", filepath.Join(s.PS4.GameDir, "covers")))
	if status := s.PS2.FPKG.Status(); status.Ready {
		checks = append(checks, ReadinessCheck{Name: "ps2_fpkg_converter", Status: "ok", Path: status.Emulator})
	} else {
		checks = append(checks, ReadinessCheck{Name: "ps2_fpkg_converter", Status: "warning", Path: status.Emulator, Detail: status.Message})
	}

	contentReady := s.PS4.Content.Running()
	contentDetail := ""
	if !contentReady {
		contentDetail = "PS4 package server is not running"
	}
	checks = append(checks, ReadinessCheck{Name: "ps4_package_server", Status: readinessStatus(contentReady), Detail: contentDetail})
	if err := s.PS4.Content.AdvertiseError(); err != nil {
		checks = append(checks, ReadinessCheck{Name: "ps4_advertise_url", Status: "warning", Detail: err.Error()})
	} else {
		checks = append(checks, ReadinessCheck{Name: "ps4_advertise_url", Status: "ok"})
	}

	report := ReadinessReport{Ready: true, Status: "ready", Checks: checks}
	warnings := false
	for _, check := range checks {
		if check.Status == "ok" {
			continue
		}
		if check.Required {
			report.Ready = false
		} else {
			warnings = true
		}
	}
	switch {
	case !report.Ready:
		report.Status = "not_ready"
	case warnings:
		report.Status = "ready_with_warnings"
	}
	return report
}

func directoryCheck(name, path string, required bool) ReadinessCheck {
	check := ReadinessCheck{Name: name, Status: "ok", Required: required, Path: path}
	info, err := os.Stat(path)
	if err != nil {
		check.Status = readinessFailure(required)
		check.Detail = err.Error()
		return check
	}
	if !info.IsDir() {
		check.Status = readinessFailure(required)
		check.Detail = fmt.Sprintf("configured path is not a directory: %s", path)
		return check
	}
	directory, err := os.Open(path)
	if err != nil {
		check.Status = readinessFailure(required)
		check.Detail = err.Error()
		return check
	}
	_, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		check.Status = readinessFailure(required)
		check.Detail = readErr.Error()
	} else if closeErr != nil {
		check.Status = readinessFailure(required)
		check.Detail = closeErr.Error()
	}
	return check
}

func readinessFailure(required bool) string {
	if required {
		return "error"
	}
	return "warning"
}

func writableDirectoryCheck(name, path string) ReadinessCheck {
	check := ReadinessCheck{Name: name, Status: "ok", Path: path}
	if err := os.MkdirAll(path, 0o755); err != nil {
		check.Status = "warning"
		check.Detail = err.Error()
		return check
	}
	probe, err := os.CreateTemp(path, ".ps3mgr-readiness-*")
	if err != nil {
		check.Status = "warning"
		check.Detail = err.Error()
		return check
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		check.Status = "warning"
		check.Detail = err.Error()
	}
	if err := os.Remove(probePath); err != nil && check.Detail == "" {
		check.Status = "warning"
		check.Detail = err.Error()
	}
	return check
}

func readinessStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "warning"
}
