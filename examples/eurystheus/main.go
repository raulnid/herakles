// Command eurystheus is a small in-memory API for herakles demos and tests:
// API key login, sessions, and projects with file upload and download.
// Eurystheus was the king who handed Heracles his labors.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxUpload = 64 << 20

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	flag.Parse()
	key := os.Getenv("EURYSTHEUS_API_KEY")
	if key == "" {
		key = "demo-api-key"
		slog.Warn("EURYSTHEUS_API_KEY not set, accepting the demo key")
	}
	slog.Info("eurystheus listening", "addr", *listen)
	srv := &http.Server{Addr: *listen, Handler: newServer(key), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

type project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
	data   []byte
}

type server struct {
	mux      *http.ServeMux
	apiKey   string
	mu       sync.Mutex
	sessions map[string]bool
	projects map[string]*project
	corrupt  atomic.Bool // tests: serve downloads with one byte flipped
}

func newServer(apiKey string) *server {
	s := &server{mux: http.NewServeMux(), apiKey: apiKey, sessions: map[string]bool{}, projects: map[string]*project{}}
	mux := s.mux
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("POST /auth/token", s.login)
	mux.HandleFunc("GET /auth/me", s.auth(s.me))
	mux.HandleFunc("POST /auth/logout", s.auth(s.logout))
	mux.HandleFunc("GET /projects", s.auth(s.list))
	mux.HandleFunc("POST /projects", s.auth(s.create))
	mux.HandleFunc("GET /projects/{id}", s.auth(s.get))
	mux.HandleFunc("GET /projects/{id}/file", s.auth(s.file))
	mux.HandleFunc("PATCH /projects/{id}", s.auth(s.rename))
	mux.HandleFunc("DELETE /projects/{id}", s.auth(s.remove))
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ua", r.UserAgent())
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) auth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		valid := ok && s.sessions[token]
		s.mu.Unlock()
		if !valid {
			fail(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next(w, r, token)
	}
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil || in.APIKey != s.apiKey {
		fail(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	token := newID() + newID()
	s.mu.Lock()
	s.sessions[token] = true
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"token": token, "expires_in": 3600})
}

func (s *server) me(w http.ResponseWriter, _ *http.Request, _ string) {
	reply(w, http.StatusOK, map[string]string{"user": "synthetic"})
}

func (s *server) logout(w http.ResponseWriter, _ *http.Request, token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) list(w http.ResponseWriter, _ *http.Request, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []project{}
	for _, p := range s.projects {
		out = append(out, *p)
	}
	reply(w, http.StatusOK, out)
}

func (s *server) create(w http.ResponseWriter, r *http.Request, _ string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	f, _, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, "multipart field file: "+err.Error())
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	sum := sha256.Sum256(data)
	p := &project{ID: newID(), Name: r.FormValue("name"), Size: len(data), SHA256: hex.EncodeToString(sum[:]), data: data}
	s.mu.Lock()
	s.projects[p.ID] = p
	s.mu.Unlock()
	reply(w, http.StatusCreated, p)
}

// find returns a copy of the project, or replies 404. rename is the only
// writer and works under the lock.
func (s *server) find(w http.ResponseWriter, r *http.Request) (project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[r.PathValue("id")]
	if !ok {
		fail(w, http.StatusNotFound, "project not found")
		return project{}, false
	}
	return *p, true
}

func (s *server) get(w http.ResponseWriter, r *http.Request, _ string) {
	if p, ok := s.find(w, r); ok {
		reply(w, http.StatusOK, p)
	}
}

func (s *server) file(w http.ResponseWriter, r *http.Request, _ string) {
	p, ok := s.find(w, r)
	if !ok {
		return
	}
	data := p.data
	if s.corrupt.Load() && len(data) > 0 {
		data = append([]byte{data[0] ^ 0xff}, data[1:]...)
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(data)
}

func (s *server) rename(w http.ResponseWriter, r *http.Request, _ string) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil || in.Name == "" {
		fail(w, http.StatusBadRequest, "body must be {\"name\": \"...\"}")
		return
	}
	s.mu.Lock()
	p, ok := s.projects[r.PathValue("id")]
	var out project
	if ok {
		p.Name = in.Name
		out = *p
	}
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "project not found")
		return
	}
	reply(w, http.StatusOK, out)
}

func (s *server) remove(w http.ResponseWriter, r *http.Request, _ string) {
	s.mu.Lock()
	_, ok := s.projects[r.PathValue("id")]
	delete(s.projects, r.PathValue("id"))
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "project not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
