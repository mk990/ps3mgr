package web

import (
	"fmt"
	"net/http"
	"strings"
)

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
