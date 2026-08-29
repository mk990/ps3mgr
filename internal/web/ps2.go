package web

import (
	"fmt"
	"net/http"
)

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
