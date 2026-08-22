// cmd/admin scaffolds the Abuse/Admin API deployment unit (ARC-004):
// "Private/privileged deployment; deny by default; no public network
// path except approved report channel." This binds only to localhost by
// default, unlike the other three commands, as a boundary reminder — a
// real deployment puts this behind an internal load balancer with no
// public route at all (docs/TECH_STACK.md's internal ALB).
//
// This is a scaffold, not a working service: it does not yet implement
// suspend/reinstate/report handlers. Those require privileged
// authentication (MFA, deny-by-default per docs/decisions/DEC-005.md)
// that has not been built — see docs/THREAT_MODEL.md Gap G1. Building
// them is separate, future work; this command exists so the deployment
// boundary is real and reviewable now, not implied by documentation
// alone.
package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// TODO(Gap G1): POST /suspend, POST /reinstate, POST /reports — all
	// require the privileged-identity/MFA path from docs/decisions/DEC-005.md,
	// none of which exists yet.

	addr := "127.0.0.1:8083" // localhost-only: no public network path (ARC-004)
	log.Printf("admin API scaffold listening on http://%s (ARC-004) — suspend/reinstate NOT implemented, see docs/THREAT_MODEL.md Gap G1", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
