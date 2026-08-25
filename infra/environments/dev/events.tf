# T9-05 / ARC-009 / ARC-010 / INT-008: committed destination-free outbox
# rows flow through DynamoDB Streams -> EventBridge Pipes -> SQS Standard.
# Delivery is at least once. The worker's conditional checkpoint is what makes
# duplicates/reordering safe; version gaps remain unacknowledged for redrive.

resource "aws_dynamodb_table" "outbox_event" {
  name         = "outbox_event"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "aggregate_key"
  range_key    = "event_key"

  stream_enabled   = true
  stream_view_type = "NEW_IMAGE"

  attribute {
    name = "aggregate_key"
    type = "S"
  }

  attribute {
    name = "event_key"
    type = "S"
  }

  attribute {
    name = "unpublished_shard"
    type = "S"
  }

  attribute {
    name = "unpublished_key"
    type = "S"
  }

  global_secondary_index {
    name            = "gsi_unpublished"
    hash_key        = "unpublished_shard"
    range_key       = "unpublished_key"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  server_side_encryption {
    enabled = true
  }

  tags = module.tags.common_tags
}

resource "aws_dynamodb_table" "worker_checkpoint" {
  name         = "worker_checkpoint"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "aggregate_key"

  attribute {
    name = "aggregate_key"
    type = "S"
  }

  server_side_encryption {
    enabled = true
  }

  tags = module.tags.common_tags
}

resource "aws_sqs_queue" "outbox_worker_dlq" {
  name                      = "url-shortener-outbox-worker-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
  tags                      = module.tags.common_tags
}

resource "aws_sqs_queue" "outbox" {
  name                       = "url-shortener-outbox"
  message_retention_seconds  = 1209600
  receive_wait_time_seconds  = 20
  visibility_timeout_seconds = 60
  sqs_managed_sse_enabled    = true
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.outbox_worker_dlq.arn
    maxReceiveCount     = 5
  })
  tags = module.tags.common_tags
}

resource "aws_sqs_queue_redrive_allow_policy" "outbox_worker_dlq" {
  queue_url = aws_sqs_queue.outbox_worker_dlq.id
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.outbox.arn]
  })
}

# A separate pipe DLQ preserves failures that occur before SQS accepts a
# worker message. Its payload is a stream-source failure record and therefore
# must not be mixed with the worker DLQ's directly replayable SQS messages.
resource "aws_sqs_queue" "outbox_pipe_dlq" {
  name                      = "url-shortener-outbox-pipe-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
  tags                      = module.tags.common_tags
}

data "aws_iam_policy_document" "pipes_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["pipes.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "outbox_pipe" {
  name               = "url-shortener-dev-outbox-pipe"
  assume_role_policy = data.aws_iam_policy_document.pipes_assume.json
  tags               = module.tags.common_tags
}

data "aws_iam_policy_document" "outbox_pipe" {
  statement {
    sid = "ReadOutboxStream"
    actions = [
      "dynamodb:DescribeStream",
      "dynamodb:GetRecords",
      "dynamodb:GetShardIterator",
      "dynamodb:ListStreams",
    ]
    resources = [aws_dynamodb_table.outbox_event.stream_arn]
  }

  statement {
    sid       = "DeliverWorkerMessages"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.outbox.arn, aws_sqs_queue.outbox_pipe_dlq.arn]
  }
}

resource "aws_iam_role_policy" "outbox_pipe" {
  name   = "outbox-stream-to-sqs"
  role   = aws_iam_role.outbox_pipe.id
  policy = data.aws_iam_policy_document.outbox_pipe.json
}

resource "aws_pipes_pipe" "outbox" {
  name     = "url-shortener-dev-outbox"
  role_arn = aws_iam_role.outbox_pipe.arn
  source   = aws_dynamodb_table.outbox_event.stream_arn
  target   = aws_sqs_queue.outbox.arn

  source_parameters {
    filter_criteria {
      filter {
        pattern = jsonencode({ eventName = ["INSERT"] })
      }
    }

    dynamodb_stream_parameters {
      starting_position                  = "TRIM_HORIZON"
      batch_size                         = 1
      maximum_batching_window_in_seconds = 0
      maximum_record_age_in_seconds      = 82800
      maximum_retry_attempts             = 10
      on_partial_batch_item_failure      = "AUTOMATIC_BISECT"
      parallelization_factor             = 1

      dead_letter_config {
        arn = aws_sqs_queue.outbox_pipe_dlq.arn
      }
    }
  }

  tags = module.tags.common_tags
}
