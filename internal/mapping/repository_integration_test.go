//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to real
// AWS DynamoDB instead, using the ambient AWS credential chain (T5-08's
// ephemeral-AWS integration workflow) — never the static "local"
// credentials below, which would silently fail against a real account.
// Run with: go test -tags=integration ./internal/mapping/...
package mapping

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func localClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	endpoint, isLocal := os.LookupEnv("DYNAMODB_ENDPOINT")
	if !isLocal {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			t.Fatalf("load AWS config: %v", err)
		}
		return dynamodb.NewFromConfig(cfg)
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

// deleteTable is real teardown for tests that create their own
// uniquely-named table — without it, repeated runs against real AWS
// (T5-08's ephemeral workflow) would accumulate orphaned tables forever.
// Best-effort: a delete failure fails the test loudly rather than
// silently leaking a real AWS resource.
func deleteTable(t *testing.T, client *dynamodb.Client, table string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			t.Errorf("cleanup: delete table %s: %v", table, err)
		}
	})
}

func freshRepo(t *testing.T) *Repository {
	t.Helper()
	client := localClient(t)
	suffix := time.Now().UnixNano()
	mappingTable := fmt.Sprintf("repo_mapping_%d", suffix)
	claimTable := fmt.Sprintf("repo_claim_%d", suffix)
	outboxTable := fmt.Sprintf("repo_outbox_%d", suffix)
	r := New(client, mappingTable, claimTable, outboxTable)
	if err := r.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	deleteTable(t, client, mappingTable)
	deleteTable(t, client, claimTable)
	deleteTable(t, client, outboxTable)
	return r
}

// TestConcurrentExactRepeatYieldsOneMapping proves FR-004/FR-005:
// at least 100 concurrent byte-identical creation requests must produce
// exactly one active mapping and one returned key value.
func TestConcurrentExactRepeatYieldsOneMapping(t *testing.T) {
	r := freshRepo(t)
	const destination = "https://example.com/very/specific/repeated/path?query=1"
	const goroutines = 100

	results := make([]CreateResult, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine proposes a different candidate short key;
			// only the winner's key should ever be stored.
			candidateKey := fmt.Sprintf("k%03d", idx)
			results[idx], errs[idx] = r.Create(context.Background(), candidateKey, destination)
		}(i)
	}
	wg.Wait()

	winners := map[string]int{}
	createdCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
		winners[results[i].ShortKey]++
		if results[i].Created {
			createdCount++
		}
	}

	if createdCount != 1 {
		t.Fatalf("expected exactly 1 goroutine to create the mapping, got %d", createdCount)
	}
	if len(winners) != 1 {
		t.Fatalf("expected all %d goroutines to agree on one short key, got %d distinct keys: %v", goroutines, len(winners), winners)
	}
	outboxItems, err := r.client.Scan(context.Background(), &dynamodb.ScanInput{
		TableName: aws.String(r.outboxTable),
		Select:    types.SelectCount,
	})
	if err != nil {
		t.Fatalf("count concurrent-repeat outbox events: %v", err)
	}
	if outboxItems.Count != 1 {
		t.Fatalf("expected exactly one outbox event for %d concurrent repeats, got %d", goroutines, outboxItems.Count)
	}
	t.Logf("PASS: %d concurrent exact-repeat requests converged on exactly one mapping and one key", goroutines)
}

// TestDistinctDestinationsGetDistinctMappings is the negative case:
// two non-identical destinations must not collide.
func TestDistinctDestinationsGetDistinctMappings(t *testing.T) {
	r := freshRepo(t)
	res1, err := r.Create(context.Background(), "keyA", "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Create(context.Background(), "keyB", "https://example.com/b")
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Created || !res2.Created {
		t.Fatalf("expected both distinct destinations to create new mappings: res1=%+v res2=%+v", res1, res2)
	}
}

// TestCreationMetadataIsAtomicAndImmutable proves DR-003/DR-004 at the
// repository boundary: lifecycle metadata is committed with the mapping,
// uses a precise UTC representation, and is not rewritten by an exact repeat
// or a rejected short-key collision.
func TestCreationMetadataIsAtomicAndImmutable(t *testing.T) {
	r := freshRepo(t)
	createdAt := time.Date(2026, time.August, 25, 9, 10, 11, 123456789, time.FixedZone("test", 7*60*60))
	r.now = func() time.Time { return createdAt }

	const (
		key         = "metaA1"
		destination = "https://example.com/creation-metadata"
	)
	created, err := r.Create(context.Background(), key, destination)
	if err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	if !created.Created || created.ShortKey != key {
		t.Fatalf("unexpected create result: %+v", created)
	}

	wantCreatedAt := createdAt.UTC().Format(time.RFC3339Nano)
	assertMetadata := func(stage string) {
		t.Helper()
		record, found, err := r.GetWithStatus(context.Background(), key)
		if err != nil {
			t.Fatalf("%s: get mapping: %v", stage, err)
		}
		if !found {
			t.Fatalf("%s: mapping missing", stage)
		}
		if record.Destination != destination || record.Status != "Active" || record.Version != 1 || record.CreatedAt != wantCreatedAt {
			t.Fatalf("%s: unexpected mapping metadata: %+v; want destination=%q status=Active version=1 createdAt=%q",
				stage, record, destination, wantCreatedAt)
		}
		parsed, err := time.Parse(time.RFC3339Nano, record.CreatedAt)
		if err != nil {
			t.Fatalf("%s: parse created_at: %v", stage, err)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("%s: created_at is not UTC: %q", stage, record.CreatedAt)
		}
	}
	assertMetadata("initial create")

	r.now = func() time.Time { return createdAt.Add(24 * time.Hour) }
	repeated, err := r.Create(context.Background(), "unused2", destination)
	if err != nil {
		t.Fatalf("exact repeat: %v", err)
	}
	if repeated.Created || repeated.ShortKey != key {
		t.Fatalf("unexpected exact-repeat result: %+v", repeated)
	}
	assertMetadata("exact repeat")
	assertOutboxMissing(t, r, "unused2")

	_, err = r.Create(context.Background(), key, "https://example.com/different")
	if !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("expected ErrKeyCollision, got %v", err)
	}
	assertMetadata("rejected key collision")
}

// TestCreateCommitsDestinationFreeOutboxEvent proves DATA-005/ADR-005's
// creation boundary: mapping, claim, and one versioned propagation intent are
// committed together. The event carries only allowlisted mapping metadata and
// never copies the sensitive destination URL.
func TestCreateCommitsDestinationFreeOutboxEvent(t *testing.T) {
	r := freshRepo(t)
	fixedTime := time.Date(2026, time.August, 25, 12, 30, 45, 123456789, time.UTC)
	r.now = func() time.Time { return fixedTime }
	const (
		key         = "outbox1"
		destination = "https://example.com/private?token=must-not-enter-outbox"
	)

	if _, err := r.Create(context.Background(), key, destination); err != nil {
		t.Fatalf("create mapping with outbox: %v", err)
	}
	event := readOutboxEvent(t, r, key)
	wantCreatedAt := fixedTime.Format(time.RFC3339Nano)
	if event.AggregateKey != key || event.AggregateVersion != 1 || event.EventType != MappingCreatedEventType ||
		event.SchemaVersion != OutboxSchemaVersion || event.CreatedAt != wantCreatedAt {
		t.Fatalf("unexpected outbox envelope: %+v", event)
	}
	if event.EventKey != "00000000000000000001#MappingCreated" {
		t.Fatalf("unexpected event key: %q", event.EventKey)
	}
	if event.UnpublishedKey != event.CreatedAt+"#"+event.EventID {
		t.Fatalf("unpublished key does not preserve time/event ordering: %+v", event)
	}
	if len(event.UnpublishedShard) != 2 || event.UnpublishedShard < "00" || event.UnpublishedShard > "15" {
		t.Fatalf("outbox shard is outside the 16-shard contract: %q", event.UnpublishedShard)
	}
	wantPayload := `{"shortKey":"outbox1","status":"Active","version":1}`
	if event.Payload != wantPayload {
		t.Fatalf("unexpected mapping-created payload: got %q want %q", event.Payload, wantPayload)
	}
	if strings.Contains(event.Payload, destination) || strings.Contains(event.Payload, "must-not-enter-outbox") {
		t.Fatalf("outbox payload leaked destination data: %q", event.Payload)
	}
}

// TestMissingOutboxTableRollsBackMappingAndClaim proves there is no dual-write
// window. If the event cannot be committed, neither authoritative item exists;
// once the dependency recovers, a retry commits all three items successfully.
func TestMissingOutboxTableRollsBackMappingAndClaim(t *testing.T) {
	r := freshRepo(t)
	broken := New(r.client, r.mappingTable, r.claimTable, "missing_outbox_"+fmt.Sprint(time.Now().UnixNano()))
	const (
		key         = "atomic1"
		destination = "https://example.com/outbox-atomicity"
	)

	if _, err := broken.Create(context.Background(), key, destination); err == nil {
		t.Fatal("expected create to fail while the outbox table is unavailable")
	}
	assertItemMissing(t, r.client, r.mappingTable, "pk", key)
	assertItemMissing(t, r.client, r.claimTable, "pk", digestOf(destination))

	result, err := r.Create(context.Background(), key, destination)
	if err != nil {
		t.Fatalf("retry after outbox recovery: %v", err)
	}
	if !result.Created || result.ShortKey != key {
		t.Fatalf("unexpected recovery result: %+v", result)
	}
	readOutboxEvent(t, r, key)
}

func readOutboxEvent(t *testing.T, r *Repository, shortKey string) OutboxEvent {
	t.Helper()
	out, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(r.outboxTable),
		Key: map[string]types.AttributeValue{
			"aggregate_key": &types.AttributeValueMemberS{Value: shortKey},
			"event_key":     &types.AttributeValueMemberS{Value: "00000000000000000001#MappingCreated"},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("read mapping-created outbox event: %v", err)
	}
	if out.Item == nil {
		t.Fatalf("mapping-created outbox event for %q is missing", shortKey)
	}
	var event OutboxEvent
	if err := attributevalue.UnmarshalMap(out.Item, &event); err != nil {
		t.Fatalf("unmarshal mapping-created outbox event: %v", err)
	}
	return event
}

func assertItemMissing(t *testing.T, client *dynamodb.Client, table, keyName, keyValue string) {
	t.Helper()
	out, err := client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            map[string]types.AttributeValue{keyName: &types.AttributeValueMemberS{Value: keyValue}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("read %s after rolled-back transaction: %v", table, err)
	}
	if out.Item != nil {
		t.Fatalf("rolled-back transaction left an item in %s: %#v", table, out.Item)
	}
}

func assertOutboxMissing(t *testing.T, r *Repository, shortKey string) {
	t.Helper()
	out, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(r.outboxTable),
		Key: map[string]types.AttributeValue{
			"aggregate_key": &types.AttributeValueMemberS{Value: shortKey},
			"event_key":     &types.AttributeValueMemberS{Value: "00000000000000000001#MappingCreated"},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("read outbox event expected to be absent: %v", err)
	}
	if out.Item != nil {
		t.Fatalf("rejected candidate %q left a partial outbox event: %#v", shortKey, out.Item)
	}
}

// TestConcurrentSameKeyDifferentDestinationsCommitsOneMapping is T9-02's
// mapping-table concurrency proof. Destination-claim contention is covered by
// TestConcurrentExactRepeatYieldsOneMapping; this test instead makes every
// destination distinct while every writer races for the same short key. The
// mapping condition must select one winner, reject every loser with
// ErrKeyCollision, and leave no claim behind for a transaction that lost.
func TestConcurrentSameKeyDifferentDestinationsCommitsOneMapping(t *testing.T) {
	r := freshRepo(t)
	const (
		key        = "sameKey"
		goroutines = 32
	)

	destinations := make([]string, goroutines)
	results := make([]CreateResult, goroutines)
	errs := make([]error, goroutines)
	start := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		destinations[i] = fmt.Sprintf("https://example.com/concurrent-key-owner/%d", i)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = r.Create(context.Background(), key, destinations[idx])
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatalf("expected one successful writer, but writers %d and %d both succeeded", winner, i)
			}
			if !results[i].Created || results[i].ShortKey != key {
				t.Fatalf("writer %d returned an invalid success result: %+v", i, results[i])
			}
			winner = i
		case errors.Is(err, ErrKeyCollision):
			// Expected for every writer that lost the mapping-table CAS.
		default:
			t.Fatalf("writer %d returned an unexpected error: %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("expected exactly one successful writer, got none")
	}

	stored, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(r.mappingTable),
		Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: key}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("strongly read winning mapping: %v", err)
	}
	storedDestination, ok := stored.Item["destination"].(*types.AttributeValueMemberS)
	if !ok || storedDestination.Value != destinations[winner] {
		t.Fatalf("expected winning destination %q, got item %#v", destinations[winner], stored.Item)
	}

	claimCount := 0
	for i, destination := range destinations {
		claim, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
			TableName:      aws.String(r.claimTable),
			Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: digestOf(destination)}},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("strongly read claim %d: %v", i, err)
		}
		if claim.Item == nil {
			continue
		}
		claimCount++
		if i != winner {
			t.Fatalf("losing writer %d left a partial destination claim", i)
		}
	}
	if claimCount != 1 {
		t.Fatalf("expected exactly one committed destination claim, got %d", claimCount)
	}
}

// TestCaseSensitiveKeysRemainDistinct proves DR-001 at the DynamoDB boundary:
// keys differing only by ASCII case remain separate partition-key values and
// resolve to their own immutable destinations.
func TestCaseSensitiveKeysRemainDistinct(t *testing.T) {
	r := freshRepo(t)

	for _, tc := range []struct {
		key         string
		destination string
	}{
		{key: "CaseA1", destination: "https://example.com/upper"},
		{key: "caseA1", destination: "https://example.com/lower"},
	} {
		result, err := r.Create(context.Background(), tc.key, tc.destination)
		if err != nil {
			t.Fatalf("Create(%q): %v", tc.key, err)
		}
		if !result.Created || result.ShortKey != tc.key {
			t.Fatalf("Create(%q) returned %+v", tc.key, result)
		}
	}

	for _, tc := range []struct {
		key         string
		destination string
	}{
		{key: "CaseA1", destination: "https://example.com/upper"},
		{key: "caseA1", destination: "https://example.com/lower"},
	} {
		destination, found, err := r.Get(context.Background(), tc.key)
		if err != nil {
			t.Fatalf("Get(%q): %v", tc.key, err)
		}
		if !found || destination != tc.destination {
			t.Fatalf("Get(%q): found=%v destination=%q, want %q", tc.key, found, destination, tc.destination)
		}
	}
}

// TestDestinationBoundaryRoundTripsExactBytes proves DR-002 at the durable
// repository boundary. The largest accepted destination is returned byte for
// byte, while limit+1 is rejected before either table receives a partial item.
func TestDestinationBoundaryRoundTripsExactBytes(t *testing.T) {
	r := freshRepo(t)
	const prefix = "https://example.com/"
	atLimit := prefix + strings.Repeat("a", maxDestinationLength-len(prefix))

	result, err := r.Create(context.Background(), "limit01", atLimit)
	if err != nil {
		t.Fatalf("Create(destination at limit): %v", err)
	}
	if !result.Created {
		t.Fatal("expected destination at the limit to create a mapping")
	}

	got, found, err := r.Get(context.Background(), result.ShortKey)
	if err != nil {
		t.Fatalf("Get(destination at limit): %v", err)
	}
	if !found || got != atLimit {
		t.Fatalf("destination round trip mismatch: found=%v got length=%d want length=%d", found, len(got), len(atLimit))
	}

	tooLong := atLimit + "b"
	if _, err := r.Create(context.Background(), "limit02", tooLong); !errors.Is(err, ErrDestinationTooLong) {
		t.Fatalf("Create(destination over limit): got %v, want ErrDestinationTooLong", err)
	}

	for table, key := range map[string]string{
		r.mappingTable: "limit02",
		r.claimTable:   digestOf(tooLong),
	} {
		out, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
			TableName:      aws.String(table),
			Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: key}},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("read %s after rejected destination: %v", table, err)
		}
		if out.Item != nil {
			t.Fatalf("rejected destination left an item in %s: %#v", table, out.Item)
		}
	}
}

// TestGetWithStatus_DistinguishesActiveFromSuspended is T8-02's core
// finding: the pre-existing Get() silently drops the stored status
// field entirely — every existing mapping looks "resolvable" to it
// regardless of status. GetWithStatus is the fix; this proves it
// actually reports Suspended, not just Active, for a real stored
// record — no suspend functionality exists yet to produce one through
// normal use, so this seeds the Suspended row directly, the same
// "poison a real record" technique T7-03 used to force a collision.
func TestGetWithStatus_DistinguishesActiveFromSuspended(t *testing.T) {
	r := freshRepo(t)

	active, err := r.Create(context.Background(), "activeK", "https://example.com/active")
	if err != nil {
		t.Fatalf("create active mapping: %v", err)
	}

	const suspendedKey = "suspendK"
	const suspendedDest = "https://example.com/suspended"
	_, err = r.client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(r.mappingTable),
		Item: map[string]types.AttributeValue{
			"pk":          &types.AttributeValueMemberS{Value: suspendedKey},
			"destination": &types.AttributeValueMemberS{Value: suspendedDest},
			"status":      &types.AttributeValueMemberS{Value: "Suspended"},
			"version":     &types.AttributeValueMemberN{Value: "2"},
		},
	})
	if err != nil {
		t.Fatalf("seed suspended mapping directly: %v", err)
	}

	activeRecord, found, err := r.GetWithStatus(context.Background(), active.ShortKey)
	if err != nil {
		t.Fatalf("GetWithStatus(active): unexpected error: %v", err)
	}
	if !found || activeRecord.Status != "Active" || activeRecord.Destination != "https://example.com/active" {
		t.Fatalf("expected an Active record for %q, got found=%v record=%+v", active.ShortKey, found, activeRecord)
	}

	suspendedRecord, found, err := r.GetWithStatus(context.Background(), suspendedKey)
	if err != nil {
		t.Fatalf("GetWithStatus(suspended): unexpected error: %v", err)
	}
	if !found || suspendedRecord.Status != "Suspended" || suspendedRecord.Destination != suspendedDest {
		t.Fatalf("expected a Suspended record for %q, got found=%v record=%+v", suspendedKey, found, suspendedRecord)
	}

	_, found, err = r.GetWithStatus(context.Background(), "totallyUnknownKey")
	if err != nil {
		t.Fatalf("GetWithStatus(unknown): unexpected error: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for a genuinely unknown key")
	}
	t.Log("PASS: GetWithStatus correctly distinguished Active, Suspended, and unknown for real stored records")
}

// TestByteEquivalenceNotSemanticEquivalence proves DR-005's stable equality
// rule across a corpus: only byte-identical destinations deduplicate. Values
// that a URL normalizer might consider equivalent remain distinct because
// DEC-006 explicitly forbids silent canonicalization.
func TestByteEquivalenceNotSemanticEquivalence(t *testing.T) {
	r := freshRepo(t)
	const destination = "https://example.com/path?a=1&b=2"

	first, err := r.Create(context.Background(), "equal01", destination)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("expected the first byte sequence to create a mapping")
	}

	repeat, err := r.Create(context.Background(), "unused1", destination)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Created || repeat.ShortKey != first.ShortKey {
		t.Fatalf("byte-identical repeat did not return the existing mapping: first=%+v repeat=%+v", first, repeat)
	}

	variants := []string{
		"https://example.com/path/?a=1&b=2",
		"https://EXAMPLE.com/path?a=1&b=2",
		"https://example.com/%70ath?a=1&b=2",
		"https://example.com/path?b=2&a=1",
		"https://example.com/path?a=1&b=2#fragment",
	}
	for i, variant := range variants {
		result, err := r.Create(context.Background(), fmt.Sprintf("diff%02d", i), variant)
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if !result.Created {
			t.Fatalf("variant %d was silently canonicalized: %q", i, variant)
		}
	}
}

// TestDigestCollisionFailsClosedWithoutPartialMapping is T9-03's required
// collision simulation. A claim is deliberately placed under another
// destination's digest, representing either a cryptographic collision or
// corrupted data. Create must verify the stored destination's full bytes,
// return ErrDigestCollision, and leave both tables unchanged.
func TestDigestCollisionFailsClosedWithoutPartialMapping(t *testing.T) {
	r := freshRepo(t)
	const (
		originalKey          = "owner01"
		candidateKey         = "newkey1"
		storedDestination    = "https://example.com/original-claim-owner"
		candidateDestination = "https://example.com/candidate-with-forced-digest-collision"
	)

	if _, err := r.Create(context.Background(), originalKey, storedDestination); err != nil {
		t.Fatalf("create original mapping: %v", err)
	}

	forcedClaim := map[string]types.AttributeValue{
		"pk":          &types.AttributeValueMemberS{Value: digestOf(candidateDestination)},
		"short_key":   &types.AttributeValueMemberS{Value: originalKey},
		"destination": &types.AttributeValueMemberS{Value: storedDestination},
	}
	if _, err := r.client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(r.claimTable),
		Item:      forcedClaim,
	}); err != nil {
		t.Fatalf("seed forced digest collision: %v", err)
	}

	_, err := r.Create(context.Background(), candidateKey, candidateDestination)
	if !errors.Is(err, ErrDigestCollision) {
		t.Fatalf("expected ErrDigestCollision, got %v", err)
	}

	candidate, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(r.mappingTable),
		Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: candidateKey}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("read rejected candidate mapping: %v", err)
	}
	if candidate.Item != nil {
		t.Fatalf("digest collision left a partial mapping: %#v", candidate.Item)
	}

	claim, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(r.claimTable),
		Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: digestOf(candidateDestination)}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("read forced collision claim: %v", err)
	}
	claimDestination, ok := claim.Item["destination"].(*types.AttributeValueMemberS)
	if !ok || claimDestination.Value != storedDestination {
		t.Fatalf("forced claim was altered after rejection: %#v", claim.Item)
	}

	original, found, err := r.Get(context.Background(), originalKey)
	if err != nil {
		t.Fatalf("read original mapping: %v", err)
	}
	if !found || original != storedDestination {
		t.Fatalf("original mapping changed: found=%v destination=%q", found, original)
	}
	assertOutboxMissing(t, r, candidateKey)
}

// TestForcedKeyCollisionRejectedSafely is T7-03's evidence: a genuine
// short-key collision (the same shortKey, two different destinations —
// something the leased-range allocator is supposed to make impossible,
// but this repository must still fail closed on rather than trust that
// promise blindly) must be rejected with ErrKeyCollision specifically,
// not misdiagnosed as an exact-repeat (ErrDigestCollision's job), and
// the original mapping must survive completely untouched — no partial
// overwrite, no corruption, BR-003 preserved.
func TestForcedKeyCollisionRejectedSafely(t *testing.T) {
	r := freshRepo(t)
	const key = "collide1"
	const original = "https://example.com/original-owner-of-this-key"
	const attacker = "https://example.com/a-completely-different-destination"

	first, err := r.Create(context.Background(), key, original)
	if err != nil {
		t.Fatalf("establishing the original mapping: unexpected error: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected the first Create for a fresh key to report Created=true")
	}

	// Force the collision: same key, a destination with a different
	// digest, so the claim-table condition passes (no digest conflict)
	// and only the mapping-table condition can be the one that fails.
	_, err = r.Create(context.Background(), key, attacker)
	if !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("expected ErrKeyCollision for a forced key collision, got %v", err)
	}

	// The original mapping must be exactly as it was — no partial
	// overwrite from the rejected attempt.
	dest, ok, getErr := r.Get(context.Background(), key)
	if getErr != nil {
		t.Fatalf("re-reading the mapping after the rejected collision: %v", getErr)
	}
	if !ok {
		t.Fatalf("expected the original mapping to still exist after the rejected collision")
	}
	if dest != original {
		t.Fatalf("expected the original destination %q to survive untouched, got %q", original, dest)
	}
	t.Logf("PASS: forced key collision on %q rejected with ErrKeyCollision; original mapping to %q survived untouched", key, original)
}

// TestReconciliationFindsNoOrphans is the local substitute for
// DR-009 restore/reconciliation evidence (DynamoDB Local has no PITR to
// exercise a true backup/restore drill against).
func TestReconciliationFindsNoOrphans(t *testing.T) {
	r := freshRepo(t)
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("recon%02d", i)
		dest := fmt.Sprintf("https://example.com/%d", i)
		if _, err := r.Create(context.Background(), key, dest); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	orphans, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("expected zero orphaned claims after normal transactional creation, found %v", orphans)
	}
	t.Log("PASS: reconciliation found zero orphaned claims across 20 transactionally-created mappings")
}
