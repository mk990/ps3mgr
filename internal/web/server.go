package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"

	"ps3mgr/internal/app"
	"ps3mgr/internal/domain"
	"ps3mgr/internal/transfers"
)

//go:embed webui/*
var assets embed.FS

type Server struct {
	app *app.Service
	mux *http.ServeMux
}

func managerList(manager *transfers.Manager) []domain.Transfer {
	if manager == nil {
		return []domain.Transfer{}
	}
	return manager.List()
}

func New(application *app.Service) *Server {
	s := &Server{app: application, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if isUnsafeMethod(r.Method) && !isSameOrigin(r) {
			writeError(w, http.StatusForbidden, fmt.Errorf("cross-origin requests are not allowed"))
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func isSameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Non-browser clients generally omit Origin and remain supported.
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /api/local-games", s.localGames)
	s.mux.HandleFunc("GET /api/local-games/{id}/icon", s.localIcon)
	s.mux.HandleFunc("POST /api/scan", s.scan)
	s.mux.HandleFunc("GET /api/consoles", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.Consoles()) })
	s.mux.HandleFunc("POST /api/consoles", s.addConsole)
	s.mux.HandleFunc("GET /api/consoles/{id}", s.console)
	s.mux.HandleFunc("GET /api/consoles/{id}/games", s.remoteGames)
	s.mux.HandleFunc("POST /api/consoles/{id}/rescan", s.rescanConsole)
	s.mux.HandleFunc("GET /api/compare/{id}", s.compare)
	s.mux.HandleFunc("POST /api/queue", s.enqueue)
	s.mux.HandleFunc("POST /api/pull", s.pull)
	s.mux.HandleFunc("GET /api/pull-queue", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, managerList(s.app.Pulls)) })
	s.mux.HandleFunc("GET /api/queue", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, append(managerList(s.app.Transfers), managerList(s.app.Pulls)...))
	})
	s.mux.HandleFunc("GET /api/queue/{id}", s.queueItem)
	s.mux.HandleFunc("POST /api/queue/{id}/cancel", s.cancel)
	s.mux.HandleFunc("POST /api/queue/{id}/retry", s.retry)
	s.mux.HandleFunc("POST /api/queue/pause", func(w http.ResponseWriter, _ *http.Request) {
		s.app.Transfers.Pause()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
	})
	s.mux.HandleFunc("POST /api/queue/resume", func(w http.ResponseWriter, _ *http.Request) {
		s.app.Transfers.Resume()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
	})
	s.mux.HandleFunc("DELETE /api/queue/completed", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{"removed": s.app.Transfers.ClearCompleted()})
	})
	s.mux.HandleFunc("GET /api/ps2/games", s.ps2Games)
	s.mux.HandleFunc("GET /api/ps2/games/{id}/cover", s.ps2Cover)
	s.mux.HandleFunc("GET /api/ps2/covers/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.app.PS2.CoverStatus())
	})
	s.mux.HandleFunc("GET /api/ps2/usb", s.ps2USB)
	s.mux.HandleFunc("GET /api/ps2/usb/status", s.ps2USBStatus)
	s.mux.HandleFunc("POST /api/ps2/usb/{id}/prepare", s.ps2PrepareUSB)
	s.mux.HandleFunc("GET /api/ps2/compare/{usb_id}", s.ps2Compare)
	s.mux.HandleFunc("POST /api/ps2/queue", s.ps2Enqueue)
	s.mux.HandleFunc("GET /api/ps2/queue", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.PS2.Queue.List()) })
	s.mux.HandleFunc("GET /api/ps2/queue/{id}", s.ps2QueueItem)
	s.mux.HandleFunc("POST /api/ps2/queue/{id}/cancel", s.ps2Cancel)
	s.mux.HandleFunc("POST /api/ps2/queue/{id}/retry", s.ps2Retry)
	s.mux.HandleFunc("GET /api/ps4/games", s.ps4Games)
	s.mux.HandleFunc("GET /api/ps4/games/{id}/cover", s.ps4Cover)
	s.mux.HandleFunc("GET /api/ps4/covers/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.app.PS4.CoverStatus())
	})
	s.mux.HandleFunc("POST /api/ps4/scan", s.ps4Scan)
	s.mux.HandleFunc("GET /api/ps4/consoles", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.PS4.Consoles()) })
	s.mux.HandleFunc("POST /api/ps4/consoles", s.ps4AddConsole)
	s.mux.HandleFunc("GET /api/ps4/consoles/{id}", s.ps4Console)
	s.mux.HandleFunc("GET /api/ps4/consoles/{id}/games", s.ps4RemoteGames)
	s.mux.HandleFunc("POST /api/ps4/consoles/{id}/rescan", s.ps4RescanConsole)
	s.mux.HandleFunc("GET /api/ps4/compare/{id}", s.ps4Compare)
	s.mux.HandleFunc("GET /api/ps4/content/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.PS4.ContentStatus()) })
	s.mux.HandleFunc("POST /api/ps4/queue", s.ps4Enqueue)
	s.mux.HandleFunc("POST /api/ps4/pull", s.ps4Pull)
	s.mux.HandleFunc("GET /api/ps4/pull-queue", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, managerList(s.app.PS4.Pulls))
	})
	s.mux.HandleFunc("POST /api/ps4/pull-queue/{id}/cancel", s.ps4PullCancel)
	s.mux.HandleFunc("POST /api/ps4/pull-queue/{id}/retry", s.ps4PullRetry)
	s.mux.HandleFunc("GET /api/ps4/queue", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.PS4.Queue.List()) })
	s.mux.HandleFunc("GET /api/ps4/queue/{id}", s.ps4QueueItem)
	s.mux.HandleFunc("POST /api/ps4/queue/{id}/cancel", s.ps4Cancel)
	s.mux.HandleFunc("POST /api/ps4/queue/{id}/retry", s.ps4Retry)
	s.mux.HandleFunc("POST /api/ps4/queue/pause", func(w http.ResponseWriter, _ *http.Request) {
		s.app.PS4.Queue.Pause()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
	})
	s.mux.HandleFunc("POST /api/ps4/queue/resume", func(w http.ResponseWriter, _ *http.Request) {
		s.app.PS4.Queue.Resume()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
	})
	s.mux.HandleFunc("DELETE /api/ps4/queue/completed", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{"removed": s.app.PS4.Queue.ClearCompleted()})
	})
	s.mux.HandleFunc("GET /api/ps5/games", s.ps5Games)
	s.mux.HandleFunc("GET /api/ps5/games/{id}/icon", s.ps5Icon)
	s.mux.HandleFunc("POST /api/ps5/scan", s.ps5Scan)
	s.mux.HandleFunc("GET /api/ps5/consoles", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.PS5.Consoles()) })
	s.mux.HandleFunc("POST /api/ps5/consoles", s.ps5AddConsole)
	s.mux.HandleFunc("GET /api/ps5/consoles/{id}", s.ps5Console)
	s.mux.HandleFunc("GET /api/ps5/consoles/{id}/games", s.ps5RemoteGames)
	s.mux.HandleFunc("POST /api/ps5/consoles/{id}/rescan", s.ps5RescanConsole)
	s.mux.HandleFunc("GET /api/ps5/compare/{id}", s.ps5Compare)
	s.mux.HandleFunc("POST /api/ps5/queue", s.ps5Enqueue)
	s.mux.HandleFunc("POST /api/ps5/pull", s.ps5Pull)
	s.mux.HandleFunc("GET /api/ps5/pull-queue", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, managerList(s.app.PS5.Pulls))
	})
	s.mux.HandleFunc("GET /api/ps5/queue", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, append(managerList(s.app.PS5.Transfers), managerList(s.app.PS5.Pulls)...))
	})
	s.mux.HandleFunc("GET /api/ps5/queue/{id}", s.ps5QueueItem)
	s.mux.HandleFunc("POST /api/ps5/queue/{id}/cancel", s.ps5Cancel)
	s.mux.HandleFunc("POST /api/ps5/queue/{id}/retry", s.ps5Retry)
	s.mux.HandleFunc("POST /api/ps5/queue/pause", func(w http.ResponseWriter, _ *http.Request) {
		s.app.PS5.Transfers.Pause()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
	})
	s.mux.HandleFunc("POST /api/ps5/queue/resume", func(w http.ResponseWriter, _ *http.Request) {
		s.app.PS5.Transfers.Resume()
		writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
	})
	s.mux.HandleFunc("DELETE /api/ps5/queue/completed", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{"removed": s.app.PS5.Transfers.ClearCompleted()})
	})
	s.mux.HandleFunc("GET /api/events", s.events)
	content, _ := fs.Sub(assets, "webui")
	for _, path := range []string{"/dashboard", "/ps2-games", "/ps2-usb", "/ps2-queue", "/ps3-games", "/ps3-consoles", "/ps3-scan", "/ps3-queue", "/ps4-games", "/ps4-consoles", "/ps4-scan", "/ps4-queue", "/ps5-games", "/ps5-consoles", "/ps5-scan", "/ps5-queue"} {
		s.mux.HandleFunc("GET "+path, s.appShell)
	}
	s.mux.Handle("GET /", http.FileServer(http.FS(content)))
}

func (s *Server) ps4Pull(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.PS4.EnqueuePull(r.Context(), request.ConsoleID, request.GameIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) ps4PullCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS4.Pulls.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps4PullRetry(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS4.Pulls.Retry(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps4Games(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS4.LocalPackages(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps4Cover(w http.ResponseWriter, r *http.Request) {
	path, ok := s.app.PS4.Cover(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

func (s *Server) ps4Scan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CIDR    string `json:"cidr"`
		Workers int    `json:"workers"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.CIDR) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("cidr is required"))
		return
	}
	items, err := s.app.PS4.Scan(r.Context(), request.CIDR, request.Workers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps4AddConsole(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IP string `json:"ip"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	console, err := s.app.PS4.AddConsole(r.Context(), request.IP)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, console)
}

func (s *Server) ps4Console(w http.ResponseWriter, r *http.Request) {
	console, ok := s.app.PS4.Console(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS4 console not found"))
		return
	}
	writeJSON(w, http.StatusOK, console)
}

func (s *Server) ps4RemoteGames(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS4.RemoteGames(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps4Compare(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS4.Compare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps4RescanConsole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.app.PS4.Console(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS4 console not found"))
		return
	}
	items, err := s.app.PS4.Compare(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	console, _ := s.app.PS4.Console(id)
	writeJSON(w, http.StatusOK, map[string]any{"console": console, "games": items})
}

func (s *Server) ps4Enqueue(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		PackageIDs  []string `json:"package_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.PS4.Enqueue(request.ConsoleID, request.PackageIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) ps4QueueItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.app.PS4.Queue.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS4 job not found"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) ps4Cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS4.Queue.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps4Retry(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS4.Queue.Retry(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps5Games(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS5.LocalGames(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps5Icon(w http.ResponseWriter, r *http.Request) {
	icon, ok := s.app.PS5.LocalIcon(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, icon)
}

func (s *Server) ps5Scan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CIDR    string `json:"cidr"`
		Workers int    `json:"workers"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.CIDR) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("cidr is required"))
		return
	}
	result, err := s.app.PS5.Scan(r.Context(), request.CIDR, request.Workers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) ps5AddConsole(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IP string `json:"ip"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	console, err := s.app.PS5.AddConsole(r.Context(), request.IP)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, console)
}

func (s *Server) ps5Console(w http.ResponseWriter, r *http.Request) {
	console, ok := s.app.PS5.Console(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS5 console not found"))
		return
	}
	writeJSON(w, http.StatusOK, console)
}

func (s *Server) ps5RemoteGames(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS5.RemoteGames(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps5Compare(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS5.Compare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps5RescanConsole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.app.PS5.Console(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS5 console not found"))
		return
	}
	items, err := s.app.PS5.Compare(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	console, _ := s.app.PS5.Console(id)
	writeJSON(w, http.StatusOK, map[string]any{"console": console, "games": items})
}

func (s *Server) ps5Enqueue(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.PS5.Enqueue(request.ConsoleID, request.GameIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) ps5QueueItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.app.PS5.Transfers.Get(r.PathValue("id"))
	if !ok {
		item, ok = s.app.PS5.Pulls.Get(r.PathValue("id"))
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS5 transfer not found"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) ps5Cancel(w http.ResponseWriter, r *http.Request) {
	err := s.app.PS5.Transfers.Cancel(r.PathValue("id"))
	if err != nil {
		err = s.app.PS5.Pulls.Cancel(r.PathValue("id"))
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps5Retry(w http.ResponseWriter, r *http.Request) {
	err := s.app.PS5.Transfers.Retry(r.PathValue("id"))
	if err != nil {
		err = s.app.PS5.Pulls.Retry(r.PathValue("id"))
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appShell(w http.ResponseWriter, _ *http.Request) {
	data, err := assets.ReadFile("webui/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) ps2Games(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS2.LocalGames(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps2Cover(w http.ResponseWriter, r *http.Request) {
	path, ok := s.app.PS2.Cover(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) ps2USB(w http.ResponseWriter, _ *http.Request) {
	items, err := s.app.PS2.USBTargets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps2USBStatus(w http.ResponseWriter, _ *http.Request) {
	status, err := s.app.PS2.USBDiscovery()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) ps2PrepareUSB(w http.ResponseWriter, r *http.Request) {
	target, err := s.app.PS2.PrepareUSB(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (s *Server) ps2Compare(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS2.Compare(r.PathValue("usb_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps2Enqueue(w http.ResponseWriter, r *http.Request) {
	var request struct {
		USBID   string   `json:"usb_id"`
		GameIDs []string `json:"game_ids"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.PS2.Enqueue(request.USBID, request.GameIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) ps2QueueItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.app.PS2.Queue.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS2 job not found"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) ps2Cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS2.Queue.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) ps2Retry(w http.ResponseWriter, r *http.Request) {
	if err := s.app.PS2.Queue.Retry(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) localGames(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.LocalGames(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) localIcon(w http.ResponseWriter, r *http.Request) {
	icon, ok := s.app.LocalIcon(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(icon)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Type", "image/png")
	_, _ = io.Copy(w, file)
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CIDR    string `json:"cidr"`
		Workers int    `json:"workers"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.CIDR) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("cidr is required"))
		return
	}
	result, err := s.app.Scan(r.Context(), request.CIDR, request.Workers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	console, ok := s.app.Console(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("console not found"))
		return
	}
	writeJSON(w, http.StatusOK, console)
}

func (s *Server) addConsole(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IP string `json:"ip"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	console, err := s.app.AddConsole(r.Context(), request.IP)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, console)
}

func (s *Server) rescanConsole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.app.Console(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("console not found"))
		return
	}
	items, err := s.app.Compare(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	console, _ := s.app.Console(id)
	writeJSON(w, http.StatusOK, map[string]any{"console": console, "games": items})
}

func (s *Server) remoteGames(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.RemoteGames(r.Context(), r.PathValue("id"), "")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.Compare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.Enqueue(request.ConsoleID, request.GameIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.EnqueuePull(r.Context(), request.ConsoleID, request.GameIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) ps5Pull(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.app.PS5.EnqueuePull(r.Context(), request.ConsoleID, request.GameIDs, request.StopOnError)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, items)
}

func (s *Server) queueItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.app.Transfers.Get(r.PathValue("id"))
	if !ok {
		item, ok = s.app.Pulls.Get(r.PathValue("id"))
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("transfer not found"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	err := s.app.Transfers.Cancel(r.PathValue("id"))
	if err != nil {
		err = s.app.Pulls.Cancel(r.PathValue("id"))
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	err := s.app.Transfers.Retry(r.PathValue("id"))
	if err != nil {
		err = s.app.Pulls.Retry(r.PathValue("id"))
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	stream, unsubscribe := s.app.Events.Subscribe(64)
	defer unsubscribe()
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-stream:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}

func decodeJSON(r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
