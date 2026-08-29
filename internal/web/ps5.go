package web

import (
	"fmt"
	"net/http"
	"strings"
)

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
