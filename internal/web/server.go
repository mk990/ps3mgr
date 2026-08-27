package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"ps3mgr/internal/app"
)

//go:embed webui/*
var assets embed.FS

type Server struct {
	app *app.Service
	mux *http.ServeMux
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
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /api/local-games", s.localGames)
	s.mux.HandleFunc("GET /api/local-games/{id}/icon", s.localIcon)
	s.mux.HandleFunc("POST /api/scan", s.scan)
	s.mux.HandleFunc("GET /api/consoles", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.Consoles()) })
	s.mux.HandleFunc("GET /api/consoles/{id}", s.console)
	s.mux.HandleFunc("GET /api/consoles/{id}/games", s.remoteGames)
	s.mux.HandleFunc("GET /api/compare/{id}", s.compare)
	s.mux.HandleFunc("POST /api/queue", s.enqueue)
	s.mux.HandleFunc("GET /api/queue", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.app.Transfers.List()) })
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
	s.mux.HandleFunc("GET /api/events", s.events)
	content, _ := fs.Sub(assets, "webui")
	s.mux.Handle("GET /", http.FileServer(http.FS(content)))
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

func (s *Server) queueItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.app.Transfers.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("transfer not found"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Transfers.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Transfers.Retry(r.PathValue("id")); err != nil {
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
