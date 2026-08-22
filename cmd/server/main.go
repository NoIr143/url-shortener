// cmd/server runs a vertical slice of the URL shortener for T4-01's
// low-fidelity UI-006 prototype and manual usability walkthroughs
// (T4-02). It uses an in-memory store, not the DynamoDB-backed
// repository (internal/mapping) — this keeps the UI demo runnable
// without Docker. A production build (cmd/creation) injects
// *mapping.Repository/*keyalloc.Allocator instead, through the same
// application.CreationUseCase this binary also uses (T6-04) — both
// satisfy webui.Store's own separate, still-unmigrated interface too.
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
	"url-shortener/internal/application"
	"url-shortener/internal/domain"
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

// memRepositoryAdapter satisfies api.Creator (T6-04) over the same
// *memStore webui.Store uses, so the JSON API and UI share one backing
// store (IR-UI-001) — mirrors cmd/creation's mappingRepositoryAdapter
// at demo scale.
type memRepositoryAdapter struct {
	store *memStore
}

func (a memRepositoryAdapter) Create(ctx context.Context, shortKey domain.ShortKey, destination domain.Destination) (domain.ShortKey, bool, error) {
	created, resolvedKey, err := a.store.Create(ctx, shortKey.String(), destination.String())
	if err != nil {
		return domain.ShortKey{}, false, err
	}
	key, err := domain.NewShortKey(resolvedKey)
	if err != nil {
		return domain.ShortKey{}, false, err
	}
	return key, created, nil
}

// sharedCounter backs both the legacy keyGen closure (webui, unchanged)
// and counterKeyGenerator (the JSON API's use case) so a key minted by
// either path can never collide with one minted by the other.
type sharedCounter struct {
	mu      sync.Mutex
	counter int64
}

func (c *sharedCounter) next() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter++
	return c.counter
}

type counterKeyGenerator struct{ counter *sharedCounter }

func (g counterKeyGenerator) Next() (domain.ShortKey, error) {
	return domain.NewShortKey("k" + strconv.FormatInt(g.counter.next(), 36))
}

func main() {
	store := newMemStore()

	counter := &sharedCounter{}
	keyGen := func() string {
		return "k" + strconv.FormatInt(counter.next(), 36)
	}

	useCase := application.NewCreationUseCase(memRepositoryAdapter{store: store}, counterKeyGenerator{counter: counter})

	createLimiter := api.NewIPRateLimiter(10, time.Minute)
	resolveLimiter := api.NewIPRateLimiter(100, time.Minute)

	createHandler := api.NewCreateHandler(useCase, createLimiter)
	resolveHandler := api.NewResolveHandler(store, resolveLimiter)
	uiHandler := webui.New(store, keyGen, createLimiter)

	mux := http.NewServeMux()
	// UI-006 (docs/UI_UX_DESIGN.md Section 3): the mandatory public creation page.
	mux.HandleFunc("GET /{$}", uiHandler.ServeCreatePage)
	mux.HandleFunc("POST /{$}", uiHandler.HandleSubmit)

	// JSON API (docs/decisions/DEC-009.md), sharing the same store/use case
	// as the UI per IR-UI-001.
	mux.HandleFunc("POST /api/v1/urls", createHandler.Create)

	// Redirect resolution (docs/ACCEPTANCE_CRITERIA.md Part B.1). Registered
	// last/most-general so it does not shadow "/" or "/api/v1/urls".
	mux.HandleFunc("GET /{shortKey}", func(w http.ResponseWriter, r *http.Request) {
		resolveHandler.Resolve(w, r, r.PathValue("shortKey"))
	})

	addr := ":8080"
	log.Printf("url-shortener prototype listening on http://localhost%s (in-memory store, T4-01 prototype — not production)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("server error:", err)
	}
}
