// cmd/server runs a vertical slice of the URL shortener for T4-01's
// low-fidelity UI-006 prototype and manual usability walkthroughs
// (T4-02). It uses an in-memory store, not the DynamoDB-backed
// repository (internal/mapping) — this keeps the UI demo runnable
// without Docker. A production build would inject *mapping.Repository
// instead; both satisfy the same api.MappingStore/webui.Store interface.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"url-shortener/internal/api"
	"url-shortener/internal/webui"
)

// memStore is a minimal in-memory implementation of api.MappingStore /
// webui.Store, sufficient for a UI prototype demo — not for production
// (no persistence, no transactional guarantees beyond a mutex).
type memStore struct {
	mu     sync.Mutex
	byKey  map[string]string
	byDest map[string]string
}

func newMemStore() *memStore {
	return &memStore{byKey: map[string]string{}, byDest: map[string]string{}}
}

func (s *memStore) Create(_ context.Context, shortKey, destination string) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byDest[destination]; ok {
		return false, existing, nil
	}
	s.byKey[shortKey] = destination
	s.byDest[destination] = shortKey
	return true, shortKey, nil
}

func (s *memStore) Get(_ context.Context, shortKey string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byKey[shortKey]
	return d, ok, nil
}

func main() {
	store := newMemStore()

	var counter int64
	var counterMu sync.Mutex
	keyGen := func() string {
		counterMu.Lock()
		defer counterMu.Unlock()
		counter++
		return "k" + strconv.FormatInt(counter, 36)
	}

	createLimiter := api.NewIPRateLimiter(10, time.Minute)
	resolveLimiter := api.NewIPRateLimiter(100, time.Minute)

	apiHandlers := api.NewHandlers(store, keyGen, createLimiter, resolveLimiter)
	uiHandler := webui.New(store, keyGen, createLimiter)

	mux := http.NewServeMux()
	// UI-006 (docs/UI_UX_DESIGN.md Section 3): the mandatory public creation page.
	mux.HandleFunc("GET /{$}", uiHandler.ServeCreatePage)
	mux.HandleFunc("POST /{$}", uiHandler.HandleSubmit)

	// JSON API (docs/decisions/DEC-009.md), sharing the same store/use case
	// as the UI per IR-UI-001.
	mux.HandleFunc("POST /api/v1/urls", apiHandlers.Create)

	// Redirect resolution (docs/ACCEPTANCE_CRITERIA.md Part B.1). Registered
	// last/most-general so it does not shadow "/" or "/api/v1/urls".
	mux.HandleFunc("GET /{shortKey}", func(w http.ResponseWriter, r *http.Request) {
		apiHandlers.Resolve(w, r, r.PathValue("shortKey"))
	})

	addr := ":8080"
	log.Printf("url-shortener prototype listening on http://localhost%s (in-memory store, T4-01 prototype — not production)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("server error:", err)
	}
}
