// cmd/worker scaffolds the Outbox/Control Worker deployment unit
// (ARC-009): "Publish committed mapping/lifecycle changes,
// invalidate/update cache, persist derived audit/event evidence, retry
// safely." It has no HTTP surface — it is a background consumer.
//
// This is a scaffold, not a working service: DynamoDB Streams ->
// EventBridge Pipes -> SQS (ADR-014) has not been built, and ADR-005
// (transactional outbox, idempotent consumers) remains Deferred per
// docs/ADR_RECONCILIATION.md — no new evidence exists for either yet.
// This command exists so the deployment boundary is real and
// reviewable now, not implied by documentation alone.
package main

import (
	"log"
	"time"
)

func main() {
	log.Print("outbox/control worker scaffold starting (ARC-009) — no queue consumer implemented yet, see docs/ADR_RECONCILIATION.md (ADR-005/ADR-014: Deferred)")
	// TODO(ADR-005/ADR-014): consume the outbox (DynamoDB Streams ->
	// EventBridge Pipes -> SQS), invalidate the redirect cache
	// (internal/cache.RedirectCache.Invalidate) on lifecycle changes, and
	// persist audit evidence (DR-006). None of this exists yet.
	for {
		time.Sleep(time.Hour)
	}
}
