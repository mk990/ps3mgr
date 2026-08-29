package web

import (
	"io/fs"
	"net/http"
)

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /api/ready", func(w http.ResponseWriter, _ *http.Request) {
		report := s.app.Readiness()
		status := http.StatusOK
		if !report.Ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, report)
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
