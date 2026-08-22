# T5-07. DEC-004 confirms mapping/audit/telemetry data stays on
# provider-managed (AWS default) KMS keys — that decision is not
# revisited here. These are customer-managed keys for two narrower
# concerns DEC-004 doesn't cover: per-secret access control (the
# account-wide default "aws/secretsmanager" key can't express
# per-secret least privilege) and container-image signing (closing the
# gap flagged in docs/poc/T5-04-images-ecr.md: "a real deployment would
# use a managed KMS-backed signing identity ... not a static keypair").

resource "aws_kms_key" "secrets" {
  description             = "Encrypts Secrets Manager secrets for url-shortener-dev"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.secrets_key_policy.json
  tags                    = module.tags.common_tags
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/url-shortener-dev-secrets"
  target_key_id = aws_kms_key.secrets.key_id
}

# A KMS key policy replaces (not supplements) the default policy, so it
# must independently grant the account root full access — otherwise the
# key becomes unmanageable by anyone, including the account owner.
data "aws_iam_policy_document" "secrets_key_policy" {
  statement {
    sid       = "EnableAccountManagement"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  statement {
    sid       = "AllowTaskRolesToDecryptViaSecretsManager"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = ["*"]

    principals {
      type = "AWS"
      identifiers = [
        module.creation.task_role_arn,
        module.redirect.task_role_arn,
      ]
    }

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["secretsmanager.${var.region}.amazonaws.com"]
    }
  }
}

# Asymmetric, sign-only — this key never decrypts anything, so a
# compromised holder of "sign" permission can forge signatures but can't
# read plaintext data. Not yet used by any CI workflow (see
# image-signing.tf) — the key and its access grant exist; wiring an
# actual signing workflow to use it (cosign's awskms:// key reference)
# is deliberate follow-up work, not built here.
resource "aws_kms_key" "image_signing" {
  description              = "Container image signing key for url-shortener (replaces the local demo keypair from T5-04)"
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P256"
  deletion_window_in_days  = 30
  tags                     = module.tags.common_tags
}

resource "aws_kms_alias" "image_signing" {
  name          = "alias/url-shortener-dev-image-signing"
  target_key_id = aws_kms_key.image_signing.key_id
}

# CloudTrail (audit.tf) needs a KMS key whose policy explicitly trusts
# the CloudTrail service principal — the default key policy alone does
# not do this, unlike the IAM-role-based access pattern used above.
resource "aws_kms_key" "cloudtrail" {
  description             = "Encrypts CloudTrail log files for url-shortener-dev"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.cloudtrail_key_policy.json
  tags                    = module.tags.common_tags
}

resource "aws_kms_alias" "cloudtrail" {
  name          = "alias/url-shortener-dev-cloudtrail"
  target_key_id = aws_kms_key.cloudtrail.key_id
}

data "aws_iam_policy_document" "cloudtrail_key_policy" {
  statement {
    sid       = "EnableAccountManagement"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  statement {
    sid       = "AllowCloudTrailToEncryptLogs"
    effect    = "Allow"
    actions   = ["kms:GenerateDataKey*"]
    resources = ["*"]

    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }

    condition {
      test     = "StringLike"
      variable = "kms:EncryptionContext:aws:cloudtrail:arn"
      values   = ["arn:aws:cloudtrail:*:${data.aws_caller_identity.current.account_id}:trail/*"]
    }
  }

  statement {
    sid       = "AllowCloudTrailToDescribeKey"
    effect    = "Allow"
    actions   = ["kms:DescribeKey"]
    resources = ["*"]

    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
  }

  statement {
    sid       = "AllowAccountToDecryptLogFiles"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:ReEncryptFrom"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["*"]
    }

    condition {
      test     = "StringEquals"
      variable = "kms:CallerAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "StringLike"
      variable = "kms:EncryptionContext:aws:cloudtrail:arn"
      values   = ["arn:aws:cloudtrail:*:${data.aws_caller_identity.current.account_id}:trail/*"]
    }
  }
}
