// cmd/admin scaffolds the Abuse/Admin API deployment unit (ARC-004):
// "Private/privileged deployment; deny by default; no public network
// path except approved report channel." It binds to localhost by
// default, unlike the other three commands, as a boundary reminder for
// local/scaffold use. The real boundary is network-level, not the bind
// address: infra/environments/dev/ecs.tf (T5-05) puts this behind an
// internal ALB with no public IP/DNS and a security group that only
// accepts traffic from inside the VPC — that's what actually enforces
// "no public bypass". ADMIN_ADDR overrides the default so the task can
// bind ":8083" in that deployment (the internal ALB reaches the task
// over its ENI, not loopback, so 127.0.0.1 would be unreachable there).
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
	"os"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// TODO(Gap G1): POST /suspend, POST /reinstate, POST /reports — all
	// require the privileged-identity/MFA path from docs/decisions/DEC-005.md,
	// none of which exists yet.

	addr := os.Getenv("ADMIN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8083" // localhost-only default: safe for local/scaffold use
	}
	log.Printf("admin API scaffold listening on http://%s (ARC-004) — suspend/reinstate NOT implemented, see docs/THREAT_MODEL.md Gap G1", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
