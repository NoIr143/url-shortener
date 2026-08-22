package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// MappingStore is the minimal surface handlers need from the creation
// path — satisfied by *mapping.Repository, expressed as an interface so
// this package does not import mapping (keeping POC-004's HTTP-layer
// tests independent of a running DynamoDB Local instance).
type MappingStore interface {
	Create(ctx context.Context, shortKey, destination string) (created bool, resolvedKey string, err error)
	Get(ctx context.Context, shortKey string) (destination string, ok bool, err error)
}

// problemDetail is the RFC 9457 shape confirmed at docs/decisions/DEC-009.md.
type problemDetail struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

func writeProblem(w http.ResponseWriter, status int, code, title, detail string, retryable bool) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Title: title, Status: status, Detail: detail, Code: code, Retryable: retryable,
	})
}

func clientIP(r *http.Request) string {
	// Boundary-test helper: real deployments read this from the edge
	// (ARC-001), not directly from RemoteAddr; POC-004 only needs a stable
	// per-caller key for the rate-limit mechanism under test.
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx >= 0 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}

// Handlers wires the confirmed acceptance criteria (docs/ACCEPTANCE_CRITERIA.md)
// to concrete HTTP behavior for POC-004's attack-corpus and rate-limit
// tests.
type Handlers struct {
	store          MappingStore
	keyGen         func() string
	createLimiter  *IPRateLimiter
	resolveLimiter *IPRateLimiter
}

func NewHandlers(store MappingStore, keyGen func() string, createLimiter, resolveLimiter *IPRateLimiter) *Handlers {
	return &Handlers{store: store, keyGen: keyGen, createLimiter: createLimiter, resolveLimiter: resolveLimiter}
}

type createRequest struct {
	Destination string `json:"destination"`
}

type createResponse struct {
	ShortKey string `json:"shortKey"`
	ShortURL string `json:"shortUrl"`
}

// Create implements POST /api/v1/urls per docs/ACCEPTANCE_CRITERIA.md Part A.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	if !h.createLimiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", "creation rate limit exceeded", true)
		return
	}

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "MALFORMED_REQUEST", "Malformed request body", err.Error(), false)
		return
	}

	if err := ValidateDestination(req.Destination); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_DESTINATION", "Destination is not acceptable", err.Error(), false)
		return
	}

	key := h.keyGen()
	created, resolvedKey, err := h.store.Create(r.Context(), key, req.Destination)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(createResponse{ShortKey: resolvedKey, ShortURL: "https://short.example/" + resolvedKey})
}

// Resolve implements GET /{shortKey} per docs/ACCEPTANCE_CRITERIA.md Part B.1.
// The destination is placed in Location only after passing ValidateDestination
// at creation time — this handler additionally strips any control character
// as defense in depth (NFR-SEC-003) even though none should ever be stored.
func (h *Handlers) Resolve(w http.ResponseWriter, r *http.Request, shortKey string) {
	if !h.resolveLimiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", "resolution rate limit exceeded", true)
		return
	}

	if err := ValidateShortKey(shortKey); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_KEY", "That key is not valid", "", false)
		return
	}

	dest, ok, err := h.store.Get(r.Context(), shortKey)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}
	if !ok {
		writeProblem(w, http.StatusNotFound, "UNKNOWN_KEY", "That link isn't valid", "", false)
		return
	}

	for _, r := range dest {
		if r < 0x20 || r == 0x7f {
			// Should be unreachable given creation-time validation; fail
			// closed rather than ever emitting a header-injection payload.
			writeProblem(w, http.StatusInternalServerError, "CORRUPT_DESTINATION", "Stored destination failed a safety check", "", false)
			return
		}
	}

	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusFound) // 302, docs/decisions/DEC-002.md
}
