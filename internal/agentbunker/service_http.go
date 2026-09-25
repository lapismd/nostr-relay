package agentbunker

import (
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"io"
	"net/http"
	"strings"
	"zooid/internal/gitforge"
)

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.handleCORS(w, r) {
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/nip46" {
		s.serveWebSocket(w, r)
		return
	}
	if path == "/api/agents/managed" {
		switch r.Method {
		case http.MethodGet:
			s.list(w, r)
		case http.MethodPost:
			s.create(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	prefix := "/api/agents/managed/"
	if !strings.HasPrefix(path, prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	agent := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "activate" && r.Method == http.MethodPost:
		s.activate(w, r, agent)
	case len(parts) == 2 && parts[1] == "connections" && r.Method == http.MethodPost:
		s.replace(w, r, agent)
	case len(parts) == 3 && parts[1] == "connections" && parts[2] == "current" && r.Method == http.MethodDelete:
		s.revoke(w, r, agent)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.delete(w, r, agent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	origin, allowed := managedBrowserOriginAllowed(r.Header.Get("Origin"), s.Config.AllowedBrowserOrigins)
	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		if !allowed {
			http.Error(w, "origin not allowed", http.StatusForbidden)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return true
	}
	return false
}

func (s *Service) authenticate(r *http.Request, body []byte) (nostr.Event, error) {
	expected := strings.TrimRight(s.Config.PublicURL, "/") + r.URL.Path
	event, err := gitforge.VerifyRequestAuth(r, expected, body, s.Now())
	if err != nil {
		return event, err
	}
	if err = s.Store.UseAuthEvent(r.Context(), event.ID.Hex(), s.Now()); err != nil {
		return event, ErrReplay
	}
	if s.AdmitOwner != nil && !s.AdmitOwner(r.Context(), event.PubKey) {
		return event, errors.New("owner is not admitted")
	}
	if _, err = s.Store.Agent(r.Context(), event.PubKey.Hex()); err == nil {
		return event, errors.New("managed agents cannot provision agents")
	}
	return event, nil
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, maxBody+1))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
