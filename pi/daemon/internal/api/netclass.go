// Package api: net-class endpoint.
//
// GET  /api/v1/netclass       -> {class, updatedAt}
// POST /api/v1/netclass {class: "..."} -> 204 on success, 400 on
//                                          invalid class, 503 if
//                                          subsystem disabled.
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleNetClass(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.netClassGet(w, r)
	case http.MethodPost, http.MethodPut:
		s.netClassSet(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) netClassGet(w http.ResponseWriter, _ *http.Request) {
	if s.providers.NetClassGet == nil {
		http.Error(w, "netclass disabled", http.StatusNotFound)
		return
	}
	class, updatedAt := s.providers.NetClassGet()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"class":     class,
		"updatedAt": updatedAt.Format(time.RFC3339),
	})
}

func (s *Server) netClassSet(w http.ResponseWriter, r *http.Request) {
	if s.providers.NetClassSet == nil {
		http.Error(w, "netclass disabled", http.StatusNotFound)
		return
	}
	var body struct {
		Class string `json:"class"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.Class = strings.ToLower(strings.TrimSpace(body.Class))
	if body.Class == "" {
		http.Error(w, "missing class field", http.StatusBadRequest)
		return
	}
	if err := s.providers.NetClassSet(body.Class); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
