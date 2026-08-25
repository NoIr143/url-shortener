package worker

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type queuedMessage struct {
	id       string
	body     string
	receives int
}

type redriveQueue struct {
	messages    []*queuedMessage
	dlq         []*queuedMessage
	maxReceives int
}

func (q *redriveQueue) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	for len(q.messages) > 0 && q.messages[0].receives >= q.maxReceives {
		q.dlq = append(q.dlq, q.messages[0])
		q.messages = q.messages[1:]
	}
	if len(q.messages) == 0 {
		return &sqs.ReceiveMessageOutput{}, nil
	}
	message := q.messages[0]
	message.receives++
	return &sqs.ReceiveMessageOutput{Messages: []types.Message{{
		MessageId:     aws.String(message.id),
		Body:          aws.String(message.body),
		ReceiptHandle: aws.String(message.id),
	}}}, nil
}

func (q *redriveQueue) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	id := aws.ToString(input.ReceiptHandle)
	for i, message := range q.messages {
		if message.id == id {
			q.messages = append(q.messages[:i], q.messages[i+1:]...)
			break
		}
	}
	return &sqs.DeleteMessageOutput{}, nil
}

func (q *redriveQueue) replayDLQ() {
	for _, message := range q.dlq {
		message.receives = 0
		q.messages = append(q.messages, message)
	}
	q.dlq = nil
}

func TestRunnerGapMovesToDLQAndReplaySucceeds(t *testing.T) {
	store := newMemoryStore()
	processor := NewProcessor(store)
	queue := &redriveQueue{
		maxReceives: 2,
		messages: []*queuedMessage{{
			id:   "gap-message",
			body: streamBody(t, "ReplayA", 2, "event-2"),
		}},
	}
	runner := NewRunner(queue, "test-queue", processor, log.New(io.Discard, "", 0))

	for range 3 {
		if _, err := runner.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.dlq) != 1 || len(queue.messages) != 0 {
		t.Fatalf("gap was not redriven: source=%d dlq=%d", len(queue.messages), len(queue.dlq))
	}

	if _, err := processor.Process(context.Background(), streamBody(t, "ReplayA", 1, "event-1")); err != nil {
		t.Fatalf("apply missing version before replay: %v", err)
	}
	queue.replayDLQ()
	processed, err := runner.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || len(queue.messages) != 0 || len(queue.dlq) != 0 {
		t.Fatalf("replay not acknowledged: processed=%d source=%d dlq=%d", processed, len(queue.messages), len(queue.dlq))
	}
	if got := store.checkpoints["ReplayA"].Version; got != 2 {
		t.Fatalf("replay checkpoint version=%d, want 2", got)
	}
}

func TestRunnerDoesNotLogRejectedMessageBody(t *testing.T) {
	const sensitiveBody = `{"destination":"https://secret.example/?token=must-not-log"}`
	queue := &redriveQueue{
		maxReceives: 5,
		messages:    []*queuedMessage{{id: "malformed-message", body: sensitiveBody}},
	}
	var logs bytes.Buffer
	runner := NewRunner(queue, "test-queue", NewProcessor(newMemoryStore()), log.New(&logs, "", 0))
	if _, err := runner.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "secret.example") || strings.Contains(logs.String(), "must-not-log") || strings.Contains(logs.String(), sensitiveBody) {
		t.Fatalf("worker log leaked rejected message body: %s", logs.String())
	}
}
