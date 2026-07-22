// Package mocktn stands in for a webMethods Trading Networks REST surface
// so the flagship workflow test can execute without external dependencies.
// It is imported only by tests and the integration harness.
package mocktn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Server struct {
	*httptest.Server

	mu       sync.Mutex
	partners map[string]partner // keyed by name
	docs     map[string]doc     // keyed by id
	seq      int
	token    string
}

type partner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type doc struct {
	DocumentID string `json:"documentId"`
	PartnerID  string `json:"partnerId"`
}

// New returns a running mock TN server. Callers should Close() it. The
// server verifies `Authorization: Bearer <token>` on every request.
func New(token string) *Server {
	s := &Server{
		partners: map[string]partner{},
		docs:     map[string]doc{},
		token:    token,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/health", s.handleHealth)
	mux.HandleFunc("/rest/partners", s.handlePartners)
	mux.HandleFunc("/rest/documents", s.handleDocuments)
	s.Server = httptest.NewServer(s.auth(mux))
	return s
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.token
		if got := r.Header.Get("Authorization"); got != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "UP"})
}

func (s *Server) handlePartners(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		name := r.URL.Query().Get("name")
		matches := []partner{}
		for _, p := range s.partners {
			if name == "" || p.Name == name {
				matches = append(matches, p)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"partners": matches,
			"total":    len(matches),
		})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.seq++
		id := "p-" + itoa(s.seq)
		p := partner{ID: id, Name: body.Name}
		s.partners[body.Name] = p
		writeJSON(w, http.StatusCreated, map[string]any{"partnerId": id, "name": body.Name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PartnerID string `json:"partnerId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.seq++
	id := "doc-" + itoa(s.seq)
	s.docs[id] = doc{DocumentID: id, PartnerID: body.PartnerID}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"documentId": id, "status": "ACCEPTED"})
}

// Partners returns a snapshot for tests to inspect.
func (s *Server) Partners() []partner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]partner, 0, len(s.partners))
	for _, p := range s.partners {
		out = append(out, p)
	}
	return out
}

// Documents returns a snapshot for tests to inspect.
func (s *Server) Documents() []doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]doc, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, d)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
	// small self-contained itoa to keep imports minimal
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
