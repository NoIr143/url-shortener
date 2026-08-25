#!/bin/sh
set -eu

dlq_url="$(awslocal sqs create-queue --queue-name url-shortener-outbox-worker-dlq --attributes MessageRetentionPeriod=1209600 --query QueueUrl --output text)"
dlq_arn="$(awslocal sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)"

awslocal sqs create-queue \
  --queue-name url-shortener-outbox \
  --attributes "{\"MessageRetentionPeriod\":\"1209600\",\"ReceiveMessageWaitTimeSeconds\":\"20\",\"VisibilityTimeout\":\"60\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$dlq_arn\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" \
  >/dev/null

awslocal sqs create-queue \
  --queue-name url-shortener-outbox-pipe-dlq \
  --attributes MessageRetentionPeriod=1209600 \
  >/dev/null
