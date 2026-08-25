# DynamoDB table contracts — mapping, lease, claim, case/audit, outbox

| Field | Value |
|---|---|
| Task | `docs/TASK_BREAKDOWN.md` T9-01 (Owner: Data/Architecture; Dependency: POC-002; DEC-004/006) |
| Traceability | `DATA-001` through `DATA-008` (except `DATA-004`, the Valkey cache — not a DynamoDB table); `ADR-004`/`ADR-007` |
| Scope | Concrete DynamoDB physical contracts (key schema, attribute types, indexes, TTL, capacity mode, item-size/limit review, cost review) for six physical tables across the five named table families (`case/audit` contains two tables). Not infrastructure-as-code — no `aws_dynamodb_table` resource exists yet for any of these (`infra/environments/dev/task-policies.tf` already references the three table ARNs below as IAM policy targets, with an explicit comment that no matching resource exists yet); provisioning them is a later, separate task. |
| Prepared | 2026-08-22 |
| Status | T9-01 complete; as-built contracts are implementation evidence, while not-yet-built contracts remain provisional and implementation-gated |

## 1. Purpose and scope

`docs/SYSTEM_DESIGN.md` §5.3 deliberately describes a **portable relational shape**, "not a selected engine," pending `ADR-004`'s benchmark. That benchmark (`POC-002`, `docs/poc/POC-002-results.md`) passed locally on 2026-08-22 for the transactional mechanism, and three of the five table families in this task's title (mapping, destination claim, lease) have since been implemented and integration-tested against real DynamoDB Local across T3-01 through T8-04. This document's job is to:

1. Record the **as-built** DynamoDB contract for the three already-implemented tables, sourced directly from the code that creates and operates them (`internal/mapping/repository.go`, `internal/keyalloc/allocator.go`) — not re-derive a contract that already exists and is already tested, and explicitly flag every place the as-built contract diverges from `docs/SYSTEM_DESIGN.md` §5.2/5.3's earlier, more elaborate description.
2. **Design** (not implement — no code for these exists yet) the three remaining physical tables in the two table families named in the task title: lifecycle audit and abuse case (`DATA-006`/`DATA-007`, backing the still-pending `T10-03`/`T10-04`) and outbox (`DATA-005`, backing the still-pending `T9-04`), informed by their `DATA-*` rows, `DEC-004`'s retention decision, and `ADR-005`/`ADR-008`'s still-Proposed mechanisms.
3. Review access patterns, keys, limits, and cost for all six physical tables, per T9-01's own evidence bar.

`DATA-003` (ID Range Lease) and `DATA-004` (Redirect Cache Entry) both appear in `docs/SYSTEM_DESIGN.md` §5.2, but only `DATA-003` is in scope here — `DATA-004` is the Valkey cache (`internal/cache`), not a DynamoDB table, and T9-01's own title says "DynamoDB ... table contracts."

## 2. As-built contracts (already implemented and integration-tested)

### 2.1 `mapping` table (`DATA-001`)

| Property | Value |
|---|---|
| Source of truth | `internal/mapping/repository.go` (`Repository.Create`, `Get`, `GetWithStatus`) |
| Partition key | `pk` (String) — the short key, exact case, 1-7 chars, `[0-9a-zA-Z]` (`domain.ShortKey`) |
| Sort key | None (single-item-per-key access pattern only) |
| Attributes | `destination` (String, ≤2048 bytes, `ErrDestinationTooLong` enforced defense-in-depth at the repository layer ahead of the table); `status` (String, `"Active"` \| `"Suspended"` today — `internal/domain.Status`); `version` (Number, starts at 1) |
| Indexes | None. No GSI on `destination` exists or is planned — `docs/SYSTEM_DESIGN.md` §5.3 already forbids this ("No global index on raw destination URL is permitted without measured size/latency/cost"); destination lookup goes through the `destination_claim` table's digest index instead (§2.2). |
| TTL | None — mappings are retained indefinitely while active (`DEC-004`). |
| Capacity mode | On-demand (`BillingModePayPerRequest`) |
| Access patterns | Eventually consistent point read by `pk` (`Get`/`GetWithStatus` use DynamoDB's default because neither sets `ConsistentRead`); conditional point write in a `TransactWriteItems` (`attribute_not_exists(pk)`) at creation |
| Concurrency/CAS | Creation only today — `attribute_not_exists(pk)` prevents two different destinations ever claiming the same key (`ErrKeyCollision`, `BR-003`). The `version` attribute exists in the schema and is exercised by nothing yet — no suspend/reinstate path exists in code (`T10-04`, still `[ ]`), so it is not yet used as a CAS fencing token in practice, only reserved for that purpose. |
| Item size | Worst case ≈ 7 (key) + 2048 (destination) + ~20 (status/version/attribute overhead) bytes ≈ 2.1 KB — far under DynamoDB's 400 KB item limit; no near-limit risk. |
| PITR | Not yet configured (no Terraform resource exists). Required at provisioning time per `DEC-003` §2.4 (data-corruption RTO <1h/RPO <5min via DynamoDB continuous backup — a materially tighter RPO than `DEC-004`'s 24h daily-snapshot figure, and the two are complementary, not substitutes). |

**Divergence from `docs/SYSTEM_DESIGN.md` §5.3's portable shape**: the design doc's `mapping` row lists a provisional `numeric_id BIGINT UNIQUE`. No such attribute exists in the as-built schema — the numeric lease ID (§2.3) is Base62-encoded into `pk` directly (`DEC-012`) and never stored separately. This is not an oversight; storing it separately would be redundant with no access pattern that needs it. Flagging per `CLAUDE.md`'s "document conflicts rather than resolving them silently" — §5.2/5.3 should be reconciled to drop the `numeric_id` column at the next `docs/SYSTEM_DESIGN.md` revision, but this document does not silently edit that file's own row.

### 2.2 `destination_claim` table (`DATA-002`)

| Property | Value |
|---|---|
| Source of truth | `internal/mapping/repository.go` (`Repository.Create`, `digestOf`) |
| Partition key | `pk` (String) — lowercase hex-encoded SHA-256 digest of the destination's exact bytes, 64 characters |
| Sort key | None |
| Attributes | `short_key` (String); `destination` (String, ≤2048 bytes — stored again here, not just referenced, specifically so a digest match can be verified against the **exact bytes**, not trusted from the digest alone, per `ADR-007`) |
| Indexes | None |
| TTL | None — a claim's lifecycle follows its mapping (`DATA-002`'s own row: "lifecycle follows mapping"), and mappings are retained indefinitely while active. |
| Capacity mode | On-demand |
| Access patterns | Conditional point write in the same `TransactWriteItems` as the mapping write (`attribute_not_exists(pk)`); eventually consistent point read by `pk` (digest) on an exact-repeat, to fetch the winning claim and verify byte equality before returning it |
| Concurrency/CAS | The `attribute_not_exists(pk)` condition is exactly what makes concurrent exact-repeats converge on one winner — proven at 100 concurrent requests in `POC-002`. |
| Item size | Same order as the mapping table (~2.1 KB worst case); the digest key itself is fixed at 32 bytes raw / 64 hex chars. |

**Divergence from `docs/SYSTEM_DESIGN.md` §5.3's portable shape (a real, more significant gap than §2.1's)**: the design doc specifies a composite key `(digest, collision_ordinal)` with a `SMALLINT collision_ordinal`, explicitly to handle a genuine SHA-256 digest collision (two *different* destinations hashing to the same digest) by giving the second one "the next ordinal" rather than rejecting it. **The as-built schema has no `collision_ordinal` attribute and no composite key at all** — `pk` is the digest alone, so a genuine digest collision cannot be given a second slot; `Repository.Create` instead returns `ErrDigestCollision` and fails the write closed (see the sentinel's own doc comment: "a SHA-256 collision or data corruption, not a legitimate exact-repeat"). This is a deliberate simplification already exercised by `internal/mapping/repository_integration_test.go`, not an accidental gap — a true SHA-256 collision is cryptographically negligible at this project's confirmed 365-billion-mapping, 10-year horizon (birthday-bound collision probability at that scale is astronomically below any other named risk in this project), and failing closed is strictly safer than an unimplemented ordinal-retry path would be. Flagged here so a future reader of `docs/SYSTEM_DESIGN.md` §5.2/5.3 does not assume the ordinal mechanism exists; that document's own `DATA-002`/§5.3 rows should be reconciled to describe the as-built fail-closed behavior at its next revision.

### 2.3 `id_lease_counter` table (`DATA-003`)

| Property | Value |
|---|---|
| Source of truth | `internal/keyalloc/allocator.go` (`Allocator.AcquireLease`, `EnsureTable`) |
| Partition key | `pk` (String) — a single fixed literal, `"GLOBAL_ID_COUNTER"`. This table holds exactly **one item**, always. |
| Sort key | None |
| Attributes | `next_start` (Number, the next unissued numeric ID, monotonically increasing); `version` (Number, fencing token, incremented on every successful lease acquisition) |
| Indexes | None |
| TTL | None |
| Capacity mode | On-demand |
| Access patterns | Strongly consistent point read (`ConsistentRead: true`) of the single counter item; conditional point update (`version` equality check) to atomically advance `next_start` and claim a lease range |
| Concurrency/CAS | `version`-as-fencing-token compare-and-set on every lease acquisition, with bounded retry on contention (`ErrFencedOut` after exhausting retries) — this is the mechanism `POC-001`/T7-02's restart/expiry tests exercised. |
| Item size | Trivial — well under 1 KB. |
| Max leasable ID space | Caller-supplied `maxID` (exclusive upper bound), e.g. `1_000_000` in tests; production sizing follows `DEC-012`'s confirmed Base62/leased-ID capacity analysis (3.52 trillion addressable at 7 chars vs. 365 billion confirmed 10-year need — ~10x headroom). |

**Divergence from `docs/SYSTEM_DESIGN.md` §5.2/5.3 (already found and flagged by T7-02, restated here for this task's own completeness, not re-litigated)**: the design doc's `DATA-003`/`id_range_lease` row describes a **per-lease-item** table (`lease_id UUID PK`, `owner_id`, `fencing_token`, `expires_at`, `status`, non-overlap invariant across many rows). The as-built table is a **single global counter item** with a version-number fencing token — no per-lease audit trail, no owner attribution, no expiry, and therefore no reclaim mechanism for an abandoned lease's unused tail. T7-02's evidence doc (`docs/poc/T7-02-lease-restart-expiry-tests.md`) already made the reasoned decision not to build reclaim, given the ~10x capacity headroom above — this document does not revisit that decision, only records that the as-built contract is the single-counter shape, and that `docs/SYSTEM_DESIGN.md` §5.2/5.3's `DATA-003`/`id_range_lease` rows still describe the unbuilt, more elaborate shape and should be reconciled at that document's next revision.

## 3. Designed contracts (not yet implemented — informs `T9-04`, `T10-03`, `T10-04`)

No code exists for any of the three physical tables below. `T9-04` ("Implement transactional outbox write boundary") and `T10-03`/`T10-04` (abuse case repository; suspension/reinstatement CAS and audit) own building them; this section is that future work's starting contract, not a substitute for it, and every value below should be treated as a proposal to validate against `ADR-005`/`ADR-008` once those move past "Proposed."

### 3.1 `outbox_event` table (`DATA-005`)

| Property | Value |
|---|---|
| Partition key | `aggregate_key` (String) — the short key the event concerns, so all events for one mapping are collocated for ordered replay |
| Sort key | `event_key` (String) — zero-padded 20-digit `aggregate_version`, `#`, then `event_type` (for example `00000000000000000002#MappingSuspended`). This realizes the portable model's unique `(aggregate_key, aggregate_version, event_type)` constraint in the primary key while retaining lexical version order. |
| Attributes | `aggregate_version` (Number, duplicated from the sort-key component for typed comparison); `event_id` (String, UUID, carried in the published envelope for consumer idempotency); `event_type` (String); `schema_version` (Number); `payload` (String, JSON — allowlisted to aggregate key/status/version metadata and never a destination); `created_at` (String, UTC ISO-8601); `published_at` (String, UTC ISO-8601, absent until published); `unpublished_shard` (String, present only until published); `unpublished_key` (String, `created_at#event_id`, present only until published); `expires_at` (Number, Unix epoch seconds, absent until published and until a post-publication retention window is approved). |
| Proposed GSI | `gsi_unpublished`: partition `unpublished_shard`, sort `unpublished_key`. The shard is deterministically derived from `event_id`; it is not one fixed literal, which would put the design-target creation workload on one GSI partition. Published items atomically remove both sparse-index attributes, so they leave the backlog index. `T9-04` must benchmark and record the shard count before implementation; changing it later requires workers to query old and new shard sets through the mixed-version window. GSI reads are eventually consistent, so the worker polls/retries and never treats an empty query as proof that no event exists. |
| TTL | `expires_at` (Number, Unix epoch) — set to `published_at` + policy retention window once published (this project has no existing decision fixing that specific window; `DEC-004` fixes retention for audit/abuse-case/telemetry but is silent on outbox specifically, since `DATA-005`'s own row only says "retained until processed plus policy window" without naming the window — flagging this as a genuinely open sub-decision for whoever implements `T9-04`, not resolving it here). |
| Capacity mode | On-demand (consistent with every other table in this project, and outbox write volume tracks mapping write volume, which `docs/COST_MODEL.md` already prices as the dominant on-demand cost driver) |
| Access patterns | Write one `Put` per lifecycle event inside the same `TransactWriteItems` as the mapping/status change (`ADR-005`), conditioned on absence of the composite primary key. Replay uses a base-table `Query` by `aggregate_key`, ordered by `event_key`. Backlog workers `Query` every configured `gsi_unpublished` shard and merge oldest-first; publishing conditionally sets `published_at`/`expires_at` and removes `unpublished_shard`/`unpublished_key` only when `published_at` is still absent. Duplicate delivery remains safe through `event_id` plus aggregate version/type at the consumer. |
| Item size | Payload is bounded by design (aggregate key/status/version only, never a destination URL) — well under 1 KB expected, nowhere near the 400 KB limit. |

### 3.2 `lifecycle_audit` and `abuse_case` tables (`DATA-006`/`DATA-007`)

The task title bundles these as "case/audit" — they are two related but distinct tables, kept separate here (as `docs/SYSTEM_DESIGN.md` §5.2/5.3 already does) since they have different access patterns and different owners (`DATA-006`: Security; `DATA-007`: Security/Product) even though an abuse case's resolution is exactly what triggers a lifecycle audit event.

**`lifecycle_audit`:**

| Property | Value |
|---|---|
| Partition key | `short_key` (String) — the mapping the audit trail concerns; audit is always queried "for this key," per `DATA-006`'s own access pattern row |
| Sort key | `event_key` (String, `occurred_at#audit_id`, where `occurred_at` is UTC ISO-8601 with fixed fractional-second precision) — time-orders the append-only log and prevents two events in the same timestamp bucket from colliding |
| Attributes | `audit_id` (String, UUID); `occurred_at` (String, same timestamp representation used in `event_key`); `actor_id` (String); `action` (String, e.g. `Suspend`/`Reinstate`); `reason` (String); `prior_version` (Number); `new_version` (Number); `correlation_id` (String) |
| Proposed GSI | `gsi_by_actor` (partition `actor_id`, sort `event_key`) — `DATA-006`'s access pattern explicitly includes "query by ... actor" alongside "by key," which the base table's `short_key`-partitioned design cannot serve without a full scan. |
| TTL | None — `DEC-004` fixes a **minimum** 1-year retention, immutable, "not deletable before retention expiry, disposed only at service retirement (`FR-024`)." A DynamoDB TTL attribute is an automatic-expiry mechanism inappropriate for a floor-only, manually-disposed retention policy — using one here would risk auto-deleting audit evidence DynamoDB, not policy, decided to keep. Disposal at retirement is a deliberate, separate, non-TTL operation (`FR-024`). |
| Capacity mode | On-demand |
| Access patterns | `Query` by `short_key` (all history for a key); `Query` the `gsi_by_actor` GSI (all actions by an actor); no updates ever — append-only, immutable after commit, matching `DATA-006`'s own row. |
| Item size | Small, bounded, structured fields only — no risk of approaching the 400 KB limit. |

**`abuse_case`:**

| Property | Value |
|---|---|
| Partition key | `case_id` (String, UUID) |
| Sort key | None — cases are not naturally hierarchical under a single parent the way audit events are under a key |
| Attributes | `short_key` (String); `reporter_ref` (String); `evidence_refs` (List\<String\>); `status` (String, e.g. `Open`/`UnderReview`/`Resolved`/`Appealed`); `decision` (String, absent until resolved); `created_at`/`resolved_at` (String, UTC ISO-8601); `version` (Number, optimistic-lock fencing token — `DATA-007`'s own row: "Optimistic version for analyst concurrency"); `expires_at` (Number, Unix epoch seconds, absent until final resolution) |
| Proposed GSI | `gsi_by_short_key` (partition `short_key`, sort `created_at`) — `DATA-007`'s access pattern is "query by case/key/status"; a case-ID point lookup needs no index, but "all cases against this key" does. `gsi_by_status` (partition `status`, sort `created_at`) similarly serves an analyst work queue ("all Open cases, oldest first") without a table scan. |
| TTL | Enable DynamoDB TTL on `expires_at`. Leave it absent while a case is open/appealed; the conditional transition to final resolution sets `expires_at = resolved_at + 1 year`. DynamoDB describes deletion as typically occurring within a few days after expiry, not as a 48-hour guarantee. `T10-03` must decide whether that asynchronous disposal lag satisfies `DEC-004` or whether a scheduled disposal/reconciliation job is also required; reads must exclude expired items during the lag. |
| Capacity mode | On-demand |
| Access patterns | Point read/write by `case_id`; conditional `UpdateItem` on `version` for analyst-concurrency CAS (`DATA-007`'s own row); `Query` both proposed GSIs for the by-key and by-status work queues. |
| Item size | `evidence_refs` contains references (URIs/IDs), never embedded evidence. The SRS does not yet define maximum count or encoded length, so `T10-03` must establish both input bounds before implementation; until then, 400 KB headroom is not proven merely by calling the list bounded. |

## 4. Access pattern, key, and limit review (T9-01's evidence bar)

| Table | Every stated access pattern servable without a `Scan`? | Key type/length within DynamoDB limits? | Item size headroom |
|---|---|---|---|
| `mapping` | Yes (point read/write by `pk`) | Yes — `pk` ≤7 chars, well under the 2 KB partition-key limit | ~2.1 KB / 400 KB (0.5%) |
| `destination_claim` | Yes (point read/write by `pk`) | Yes — `pk` fixed 64 hex chars | ~2.1 KB / 400 KB (0.5%) |
| `id_lease_counter` | Yes (single-item point read/write) | Yes — fixed literal `pk` | <1 KB / 400 KB |
| `outbox_event` (designed) | Yes, given base-table aggregate replay plus the sharded sparse `gsi_unpublished` backlog index | Yes — composite key components are bounded and far below DynamoDB's 2 KB partition/1 KB sort-key limits | <1 KB / 400 KB expected (payload deliberately excludes destinations); exact payload cap belongs to `T9-04`'s event schema |
| `lifecycle_audit` (designed) | Yes, given the proposed `gsi_by_actor` GSI | Yes, with the `occurred_at#audit_id` sort-key refinement noted in §3.2 | Small, bounded |
| `abuse_case` (designed) | Yes, given the proposed `gsi_by_short_key`/`gsi_by_status` GSIs | Yes | Conditional — `T10-03` must set evidence-reference count/length limits |

No table in this document requires a `Scan` for any access pattern named in its `DATA-*` row, once the proposed GSIs for the three not-yet-built tables are in place — consistent with `docs/SYSTEM_DESIGN.md` §5.3's existing constraint that the redirect and creation paths in particular must stay point-read/point-write only.

## 5. Cost review

`docs/COST_MODEL.md` already prices DynamoDB on-demand as a system-wide line item (~$0-2/mo at near-zero traffic, "the single largest line item" at the design-target sustained peak) but does not break cost out per table. At the level this document can respond to that gap without re-deriving `docs/COST_MODEL.md`'s own analysis:

- `mapping` and `destination_claim` dominate cost at scale — one transactional write to each per new creation. Mapping-read cost is cache-hit-rate dependent: repository-only operation currently performs one eventually consistent `GetItem` per redirect, while the proposed T9-06 cache-aside path performs a mapping read only on cache miss/fallback. Cost modeling must not price both shapes as though they were the same.
- `id_lease_counter` is negligible — one write per **lease acquisition** (batched, not per mapping — a lease covers many IDs), not one write per mapping created.
- The three designed tables are also negligible at this project's confirmed near-zero expected real traffic (`docs/research/T1-02-business-baseline-outcomes.md`): `outbox_event` writes and its sparse-GSI writes track mapping/status changes; `lifecycle_audit`/`abuse_case` writes only happen on privileged lifecycle actions, which are rare by construction (`FR-017-020`), not driven by public redirect traffic.
- No table in this document needs provisioned/reserved capacity — on-demand is consistent across all six physical tables, matching every already-implemented table and `docs/TECH_STACK.md`'s existing DynamoDB selection.

This section reviews cost at the shape level `docs/COST_MODEL.md` established; it does not produce new dollar figures, and inherits that document's own accuracy caveat (approximate, not live-verified against current AWS pricing).

## 6. Effect on baseline documents

- `docs/TASK_BREAKDOWN.md`: T9-01 checked, referencing this document.
- No change made to `docs/SYSTEM_DESIGN.md` itself — §5.2/§5.3's `DATA-002`/`DATA-003` rows still describe the more elaborate, unbuilt shapes (composite claim key with collision ordinal; per-lease-item lease table) that §2.2/§2.3 above found diverge from the as-built code. Per `CLAUDE.md`'s "preserve stable IDs and document conflicts rather than resolving them silently," those rows are flagged here, not silently edited; reconciling `docs/SYSTEM_DESIGN.md` itself is a separate, explicit editorial action for whoever next revises that document.
- No production code changed. This is a pure documentation task.

## 7. Recommendation and residual risk

**Conditionally sufficient to inform `T9-04`/`T10-03`/`T10-04` and a future Terraform provisioning task.** The table/key/index shapes are defined, but each future task must close its named input-bound, retention, or scale parameter before writing a persisted contract. Residual gaps, all explicitly flagged above rather than silently resolved:

- The outbox's exact post-publication retention window and sparse-index shard count remain open — `DATA-005` does not fix either number. `T9-04` owns the benchmark/decision and may not ship with an unbounded table or a single hot backlog partition. The event-type composability issue is closed here by the `aggregate_version#event_type` sort-key contract.
- `T10-03` must define `evidence_refs` count/length bounds and decide whether DynamoDB TTL's asynchronous deletion lag satisfies DEC-004's disposal wording; otherwise it must add a scheduled disposal/reconciliation mechanism.
- The existing exact-repeat conflict path reads the winning destination claim with an eventually consistent `GetItem`. Immediately after a concurrent winner commits, that lookup may transiently miss and safely return an error rather than the existing key. This does not violate key integrity, but T9-03's closure audit should either request a strongly consistent read or prove the current retry behavior meets DEC-006/DEC-009.
- The as-built `mapping`/`destination_claim`/`id_lease_counter` divergences from `docs/SYSTEM_DESIGN.md` §5.2/5.3 (§2.1-§2.3) are flagged, not fixed at the source document — a reconciliation edit to `docs/SYSTEM_DESIGN.md` itself remains outstanding.
- No table here has PITR/backup actually configured yet (no Terraform resource exists for any of the three as-built tables); this document records the requirement (`DEC-003`/`DEC-004`) for whoever writes that Terraform, it does not configure it.
- Per-table dollar costs are not independently modeled beyond the shape-level review in §5 — `docs/COST_MODEL.md`'s own accuracy caveat applies unchanged.

## 8. Vendor facts checked for this review

- AWS DynamoDB constraints: partition keys are at most 2,048 bytes, sort keys at most 1,024 bytes, and an item (attribute names included) at most 400 KB: <https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html>.
- DynamoDB table reads are eventually consistent by default; strong consistency requires `ConsistentRead: true`, and GSIs support only eventual consistency: <https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.ReadConsistency.html>.
- TTL uses a Number containing Unix epoch seconds and expired items are typically deleted within a few days, not at a guaranteed exact time: <https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/TTL.html>.
