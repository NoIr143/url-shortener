package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type SQSClient interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type Runner struct {
	queue             SQSClient
	queueURL          string
	processor         *Processor
	logger            *log.Logger
	waitTime          int32
	visibilityTimeout int32
}

func NewRunner(queue SQSClient, queueURL string, processor *Processor, logger *log.Logger) *Runner {
	return &Runner{queue: queue, queueURL: queueURL, processor: processor, logger: logger, waitTime: 20, visibilityTimeout: 60}
}

// PollOnce receives and handles one bounded batch. Failed messages are never
// deleted: SQS visibility timeout and redrive policy own retry/DLQ behavior.
func (r *Runner) PollOnce(ctx context.Context) (int, error) {
	output, err := r.queue.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(r.queueURL),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     r.waitTime,
		VisibilityTimeout:   r.visibilityTimeout,
	})
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, message := range output.Messages {
		result, err := r.processor.Process(ctx, aws.ToString(message.Body))
		if err != nil {
			r.logger.Printf("worker message failed message_id=%q event_id=%q event_type=%q version=%d error=%v",
				aws.ToString(message.MessageId), result.Event.EventID, result.Event.EventType, result.Event.AggregateVersion, err)
			continue
		}
		if _, err := r.queue.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl:      aws.String(r.queueURL),
			ReceiptHandle: message.ReceiptHandle,
		}); err != nil {
			return processed, err
		}
		processed++
		r.logger.Printf("worker message acknowledged message_id=%q event_id=%q event_type=%q version=%d outcome=%s",
			aws.ToString(message.MessageId), result.Event.EventID, result.Event.EventType, result.Event.AggregateVersion, result.Outcome)
	}
	return processed, nil
}

func (r *Runner) Run(ctx context.Context) error {
	for {
		if _, err := r.PollOnce(ctx); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			r.logger.Printf("worker queue poll failed error=%v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}
