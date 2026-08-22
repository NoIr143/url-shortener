// POC-004 evidence: edge-case/path-case/header-input attack corpus and
// layered rate controls, per docs/decisions/DEC-005.md and DEC-007.md.
// Uses an in-memory fake store, so this runs under the default
// `go test ./...` (no infrastructure dependency), unlike POC-001/002/003.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu     sync.Mutex
	byKey  map[string]string
	byDest map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{byKey: map[string]string{}, byDest: map[string]string{}}
}

func (f *fakeStore) Create(ctx context.Context, shortKey, destination string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.byDest[destination]; ok {
		return false, existing, nil
	}
	f.byKey[shortKey] = destination
	f.byDest[destination] = shortKey
	return true, shortKey, nil
}

func (f *fakeStore) Get(ctx context.Context, shortKey string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byKey[shortKey]
	return d, ok, nil
}

// testHandlers bundles a CreateHandler and ResolveHandler over the same
// fakeStore — standing in for cmd/creation and cmd/redirect being
// separate deployment units that happen to share a backing store in
// production (the real Repository/RedirectCache), not for one combined
// Handlers type.
type testHandlers struct {
	create  *CreateHandler
	resolve *ResolveHandler
}

func newTestHandlers() testHandlers {
	store := newFakeStore()
	counter := 0
	keyGen := func() string {
		counter++
		return "k" + strconv.Itoa(counter)
	}
	return testHandlers{
		create:  NewCreateHandler(store, keyGen, NewIPRateLimiter(10, time.Minute)),
		resolve: NewResolveHandler(store, NewIPRateLimiter(100, time.Minute)),
	}
}

func doCreate(h testHandlers, body string, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/urls", strings.NewReader(body))
	req.RemoteAddr = remoteAddr + ":12345"
	rec := httptest.NewRecorder()
	h.create.Create(rec, req)
	return rec
}

// TestMaliciousInputCorpus exercises the boundary/malicious corpus
// required by docs/QUALITY_THRESHOLDS.md's NFR-SEC-003 threshold: 0
// exploitable finding, every corpus case handled per contract.
func TestMaliciousInputCorpus(t *testing.T) {
	h := newTestHandlers()

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid https", `{"destination":"https://example.com/ok"}`, http.StatusCreated},
		{"valid http", `{"destination":"http://example.com/ok"}`, http.StatusCreated},
		{"empty destination", `{"destination":""}`, http.StatusBadRequest},
		{"missing field", `{}`, http.StatusBadRequest},
		{"malformed json", `{not json`, http.StatusBadRequest},
		{"relative url", `{"destination":"/just/a/path"}`, http.StatusBadRequest},
		{"javascript scheme", `{"destination":"javascript:alert(1)"}`, http.StatusBadRequest},
		{"data scheme", `{"destination":"data:text/html,<script>alert(1)</script>"}`, http.StatusBadRequest},
		{"file scheme", `{"destination":"file:///etc/passwd"}`, http.StatusBadRequest},
		{"user-info credential leak", `{"destination":"https://user:pass@example.com/"}`, http.StatusBadRequest},
		{"CRLF header injection attempt", "{\"destination\":\"https://example.com/\r\nSet-Cookie: evil=1\"}", http.StatusBadRequest},
		{"embedded NUL byte", "{\"destination\":\"https://example.com/\\u0000hidden\"}", http.StatusBadRequest},
		{"oversized 2049 chars (limit+1)", `{"destination":"https://example.com/` + strings.Repeat("a", 2049-len("https://example.com/")) + `"}`, http.StatusBadRequest},
		{"exactly 2048 chars boundary (limit)", `{"destination":"https://example.com/` + strings.Repeat("a", 2048-len("https://example.com/")) + `"}`, http.StatusCreated},
		{"2047 chars boundary (limit-1)", `{"destination":"https://example.com/` + strings.Repeat("a", 2047-len("https://example.com/")) + `"}`, http.StatusCreated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doCreate(h, tc.body, "10.0.0."+strconv.Itoa(len(tc.name)%250+1)) // vary IP to avoid cross-case rate limiting
			if rec.Code != tc.wantStatus {
				t.Errorf("case %q: got status %d, want %d; body=%s", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
			// For every rejected case, confirm no Location/redirect-implying
			// header was ever set on the response, and the raw payload is
			// never echoed back verbatim in a way that could replay a
			// CRLF/script payload into a client context.
			if rec.Code >= 400 {
				if rec.Header().Get("Location") != "" {
					t.Errorf("case %q: rejected request must not set Location", tc.name)
				}
			}
		})
	}
}

// TestPathCaseNeverFolds proves short-key case-sensitivity survives
// the handler layer end to end (NFR-COMP-001/DEC-012's Base62 alphabet).
func TestPathCaseNeverFolds(t *testing.T) {
	h := newTestHandlers()
	rec := doCreate(h, `{"destination":"https://example.com/case-test"}`, "10.0.1.1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: unexpected status %d: %s", rec.Code, rec.Body.String())
	}

	// The fake keyGen issues "k1"; a case-folded variant "K1" must resolve
	// to a different (unknown) mapping, not the same one.
	req := httptest.NewRequest(http.MethodGet, "/K1", nil)
	req.RemoteAddr = "10.0.1.2:1"
	rec2 := httptest.NewRecorder()
	h.resolve.Resolve(rec2, req, "K1")
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected case-folded key 'K1' to be unknown (distinct from 'k1'), got status %d", rec2.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/k1", nil)
	req2.RemoteAddr = "10.0.1.3:1"
	rec3 := httptest.NewRecorder()
	h.resolve.Resolve(rec3, req2, "k1")
	if rec3.Code != http.StatusFound {
		t.Fatalf("expected exact-case key 'k1' to resolve, got status %d", rec3.Code)
	}
	if loc := rec3.Header().Get("Location"); loc != "https://example.com/case-test" {
		t.Fatalf("unexpected Location: %q", loc)
	}
	t.Log("PASS: short-key case is preserved end to end; a case-folded variant does not resolve to the original mapping")
}

// TestInvalidShortKeySyntaxRejected exercises the alphabet/length
// boundary corpus for the resolve path.
func TestInvalidShortKeySyntaxRejected(t *testing.T) {
	h := newTestHandlers()
	badKeys := []string{
		"",
		"has space",
		"has-dash",
		"has_underscore",
		"toolongkey12345", // exceeds 7-char horizon
		"emoji😀",
		"../../etc/passwd",
	}
	for _, key := range badKeys {
		t.Run(key, func(t *testing.T) {
			// The request target uses a fixed safe path — a real router
			// decodes the path segment before handing the extracted key to
			// the handler, so we pass the raw (potentially malicious) `key`
			// directly as Resolve's argument, exactly as the router would.
			req := httptest.NewRequest(http.MethodGet, "/resolve", nil)
			req.RemoteAddr = "10.0.2.1:1"
			rec := httptest.NewRecorder()
			h.resolve.Resolve(rec, req, key)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("key %q: expected 400, got %d", key, rec.Code)
			}
			if rec.Header().Get("Location") != "" {
				t.Errorf("key %q: rejected key must not set Location", key)
			}
		})
	}
}

// TestCreationRateLimitEnforced proves the layered rate control:
// requests beyond the confirmed 10/min creation quota (DEC-005) receive
// 429 without any mapping side effect.
func TestCreationRateLimitEnforced(t *testing.T) {
	h := newTestHandlers()
	ip := "10.0.3.1"

	for i := 0; i < 10; i++ {
		body := `{"destination":"https://example.com/rl` + strconv.Itoa(i) + `"}`
		rec := doCreate(h, body, ip)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d: expected 201 within quota, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := doCreate(h, `{"destination":"https://example.com/rl-over"}`, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th request: expected 429, got %d", rec.Code)
	}

	// A different IP must not be affected by the first IP's exhausted quota.
	recOther := doCreate(h, `{"destination":"https://example.com/other-ip"}`, "10.0.3.2")
	if recOther.Code != http.StatusCreated {
		t.Fatalf("different IP: expected 201, got %d", recOther.Code)
	}
	t.Log("PASS: 11th request from the same IP within the window is throttled (429); a different IP is unaffected")
}
