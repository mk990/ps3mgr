package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
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
