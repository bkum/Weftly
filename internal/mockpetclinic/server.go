// Package mockpetclinic stands in for the public Spring PetClinic REST API
// (github.com/spring-petclinic/spring-petclinic-rest) so the flagship
// workflow test can execute without external services. It implements the
// small subset of endpoints the example workflow touches — health, owners
// (list/create), pets (add), and visits (schedule).
//
// This is generic on purpose: the value of the flagship test is that it
// exercises every core Weftly mechanism (http, if:, run+jq handoff,
// template, summary, upload) against a realistic REST shape, not that it
// speaks any one vendor's protocol.
package mockpetclinic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
)

type Server struct {
	*httptest.Server

	mu     sync.Mutex
	owners map[int]Owner // by id
	pets   map[int]Pet   // by id
	visits map[int]Visit // by id
	seq    int
	apiKey string
}

type Owner struct {
	ID        int    `json:"id"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	City      string `json:"city,omitempty"`
	Telephone string `json:"telephone,omitempty"`
}

type Pet struct {
	ID       int    `json:"id"`
	OwnerID  int    `json:"ownerId"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	BirthDay string `json:"birthDay,omitempty"`
}

type Visit struct {
	ID          int    `json:"id"`
	PetID       int    `json:"petId"`
	Date        string `json:"date"`
	Description string `json:"description"`
}

// New returns a running PetClinic mock. Callers should Close() it. The
// server verifies the X-API-Key header when a non-empty apiKey is passed.
func New(apiKey string) *Server {
	s := &Server{
		owners: map[int]Owner{},
		pets:   map[int]Pet{},
		visits: map[int]Visit{},
		apiKey: apiKey,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/owners", s.handleOwners)
	mux.HandleFunc("/api/owners/", s.handleOwnerSubtree)
	s.Server = httptest.NewServer(s.auth(mux))
	return s
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != s.apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "UP"})
}

func (s *Server) handleOwners(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		lastName := r.URL.Query().Get("lastName")
		var matches []Owner
		for _, o := range s.owners {
			if lastName == "" || strings.EqualFold(o.LastName, lastName) {
				matches = append(matches, o)
			}
		}
		if matches == nil {
			matches = []Owner{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"owners": matches,
			"total":  len(matches),
		})
	case http.MethodPost:
		var body Owner
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.LastName) == "" {
			http.Error(w, "bad request: lastName required", http.StatusBadRequest)
			return
		}
		s.seq++
		body.ID = s.seq
		s.owners[body.ID] = body
		writeJSON(w, http.StatusCreated, body)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

var (
	// /api/owners/{ownerId}/pets
	petsPath = regexp.MustCompile(`^/api/owners/(\d+)/pets/?$`)
	// /api/owners/{ownerId}/pets/{petId}/visits
	visitsPath = regexp.MustCompile(`^/api/owners/(\d+)/pets/(\d+)/visits/?$`)
)

func (s *Server) handleOwnerSubtree(w http.ResponseWriter, r *http.Request) {
	if m := visitsPath.FindStringSubmatch(r.URL.Path); m != nil {
		s.handleVisits(w, r, atoi(m[1]), atoi(m[2]))
		return
	}
	if m := petsPath.FindStringSubmatch(r.URL.Path); m != nil {
		s.handlePets(w, r, atoi(m[1]))
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handlePets(w http.ResponseWriter, r *http.Request, ownerID int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.owners[ownerID]; !ok {
		http.Error(w, "unknown owner", http.StatusNotFound)
		return
	}
	var body Pet
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, "bad request: pet name required", http.StatusBadRequest)
		return
	}
	s.seq++
	body.ID = s.seq
	body.OwnerID = ownerID
	s.pets[body.ID] = body
	writeJSON(w, http.StatusCreated, body)
}

func (s *Server) handleVisits(w http.ResponseWriter, r *http.Request, ownerID, petID int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pet, ok := s.pets[petID]
	if !ok || pet.OwnerID != ownerID {
		http.Error(w, "unknown pet for owner", http.StatusNotFound)
		return
	}
	var body Visit
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.seq++
	body.ID = s.seq
	body.PetID = petID
	s.visits[body.ID] = body
	writeJSON(w, http.StatusCreated, body)
}

// Owners / Pets / Visits: snapshot helpers for tests.
func (s *Server) Owners() []Owner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Owner, 0, len(s.owners))
	for _, o := range s.owners {
		out = append(out, o)
	}
	return out
}

func (s *Server) Pets() []Pet {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Pet, 0, len(s.pets))
	for _, p := range s.pets {
		out = append(out, p)
	}
	return out
}

func (s *Server) Visits() []Visit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Visit, 0, len(s.visits))
	for _, v := range s.visits {
		out = append(out, v)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
