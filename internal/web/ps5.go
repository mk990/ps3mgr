package web

import (
	"fmt"
	"net/http"
	"strings"

	"ps3mgr/internal/fpkgi"
	"ps3mgr/internal/transfers"
)

func (s *Server) ps5Games(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS5.LocalGames(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) ps5Convert(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GameID  string   `json:"game_id"`
		GameIDs []string `json:"game_ids"`
		Format  string   `json:"format"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.GameIDs != nil {
		if request.GameID != "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("choose game_ids or game_id"))
			return
		}
		items, err := s.app.PS5.EnqueueConversions(r.Context(), request.GameIDs, request.Format)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, items)
		return
	}
	name, err := s.app.PS5.ConvertFolder(r.Context(), request.GameID, request.Format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"filename": name})
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
	if err := decodeJSON(w, r, &request); err != nil {
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
	if err := decodeJSON(w, r, &request); err != nil {
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
	if err := decodeJSON(w, r, &request); err != nil {
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

func (s *Server) ps5JobManager(id string) (*transfers.Manager, bool) {
	for _, manager := range []*transfers.Manager{s.app.PS5.Transfers, s.app.PS5.Pulls, s.app.PS5.Conversions} {
		if _, ok := manager.Get(id); ok {
			return manager, true
		}
	}
	return nil, false
}

func (s *Server) ps5QueueItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if manager, found := s.ps5JobManager(id); found {
		if item, ok := manager.Get(id); ok {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("PS5 job not found"))
}

func (s *Server) ps5QueueAction(w http.ResponseWriter, r *http.Request, action func(*transfers.Manager, string) error) {
	id := r.PathValue("id")
	manager, found := s.ps5JobManager(id)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("PS5 job not found"))
		return
	}
	if err := action(manager, id); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ps5Cancel(w http.ResponseWriter, r *http.Request) {
	s.ps5QueueAction(w, r, (*transfers.Manager).Cancel)
}
func (s *Server) ps5Retry(w http.ResponseWriter, r *http.Request) {
	s.ps5QueueAction(w, r, func(manager *transfers.Manager, id string) error {
		if manager == s.app.PS5.Conversions {
			return s.app.PS5.RetryConversion(r.Context(), id)
		}
		return manager.Retry(id)
	})
}

func (s *Server) ps5Pull(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ConsoleID   string   `json:"console_id"`
		GameIDs     []string `json:"game_ids"`
		StopOnError bool     `json:"stop_on_error"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
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

// ps5FPKGi exports the PS5 library's .pkg files as an FPKGi content list.
func (s *Server) ps5FPKGi(w http.ResponseWriter, r *http.Request) {
	items, err := s.app.PS5.FPKGiItems(r.Context(), s.app.FPKGiAdvertiser)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	fpkgi.Write(w, r, items, "ps5.json")
}
