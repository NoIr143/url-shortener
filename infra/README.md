# Infrastructure (OpenTofu)

Per `ADR-015`: OpenTofu, remote encrypted state, no long-lived CI keys. This directory currently covers **T5-02 through T5-07's scope** — the remote-state backend, root environment scaffold, GitHub Actions OIDC trust, ECR repositories, a VPC/ALB/ECS-Fargate baseline, a Route 53/ACM/CloudFront/WAF edge, and KMS/Secrets Manager/task-role/audit baseline. `T5-01` through `T5-07` are all now covered; further environment resources land here as later tasks (T6+) need them.

**`environments/dev` requires a `domain_name` variable with no default** (`variables.tf`) — no domain has ever been registered or chosen for this project. Nothing here can even `plan`, let alone `apply`, until a real value is supplied at that time.

## Layout

```
infra/
  bootstrap/                  One-time: creates the S3 state bucket + DynamoDB lock
                               table, and the account's GitHub Actions OIDC provider
                               (a singleton — only one per issuer URL per account).
                               Uses LOCAL state (chicken-and-egg — nothing else exists
                               yet to point a remote backend at).
  environments/
    dev/                       Root module for the dev environment. Remote S3 backend.
                               Includes the dev-scoped GitHub Actions IAM role, four ECR
                               repositories, a three-AZ VPC, public+internal ALBs, an
                               ECS/Fargate cluster with one service per deployment unit,
                               the edge (Route 53/ACM/CloudFront/WAF), three KMS keys, one
                               Secrets Manager secret, real least-privilege DynamoDB
                               policies on two task roles, an image-signing CI role, and
                               CloudTrail/GuardDuty/Security Hub.
  modules/
    tags/                      Standard tag map, shared by every environment.
    github-actions-role/       Reusable OIDC-trust IAM role for one workflow/environment.
    ecr-repository/            Reusable ECR repository (immutable tags, scan-on-push).
    vpc/                       Three-AZ VPC: public+private subnets, IGW, single NAT GW.
    alb/                       Reusable ALB (internet-facing or internal), HTTP-only for now.
    ecs-service/               Reusable Fargate service: task def, execution/task IAM
                               roles, security group, optional ALB target group/rule.
    edge/                      Route 53 hosted zone, ACM certificate (DNS-validated),
                               CloudFront distribution, and a WAFv2 Web ACL. Requires a
                               second, us-east-1-pinned AWS provider (CloudFront/ACM
                               constraint, independent of the environment's own region).

.github/workflows/
  terraform-plan.yml           Runs `tofu plan` for infra/environments/dev on PRs that
                               touch infra/, authenticated via OIDC — no AWS key stored
                               in GitHub.
  image-scan.yml                Builds and Trivy-scans each deployment-unit image on PRs
                               that touch the Dockerfile/cmd/internal — fails on any
                               CRITICAL/HIGH finding.
```

## Workflow

**1. Bootstrap the state backend (once, ever, per AWS account):**

```bash
cd infra/bootstrap
tofu init
tofu plan     # review — creates an S3 bucket + DynamoDB table, nothing else
tofu apply
tofu output   # note state_bucket and lock_table
```

**2. Point an environment at that backend:**

```bash
cd infra/environments/dev
cp backend.hcl.example backend.hcl   # then fill in the real bucket/table from step 1
tofu init -backend-config=backend.hcl
tofu plan
tofu apply
```

**3. Tear down (reverse order — environment first, bootstrap last):**

```bash
cd infra/environments/dev && tofu destroy
cd ../../bootstrap && tofu destroy   # only if you're done with this AWS account entirely
```

## GitHub Actions OIDC

`terraform-plan.yml` needs three repository variables (Settings > Secrets and variables > Actions > Variables — plain variables, not secrets, since none of these are sensitive) before it will run successfully:

| Variable | Value |
|---|---|
| `DEV_TERRAFORM_PLAN_ROLE_ARN` | `dev_plan_role_arn` output from `infra/environments/dev` after it's been applied |
| `TF_STATE_BUCKET` | `state_bucket` output from `infra/bootstrap` |
| `TF_LOCK_TABLE` | `lock_table` output from `infra/bootstrap` |

None of these exist yet — the workflow is written and lint-clean (`actionlint`), but won't successfully run until `bootstrap` and `environments/dev` have actually been applied and these variables set. That's expected given nothing has been applied (see below), not a bug.

The IAM role's trust policy requires the OIDC token's `sub` claim to be exactly `repo:NoIr143/url-shortener:environment:dev` — a run of this workflow from a fork, a different branch, or without the `environment: dev` line in the job cannot assume the role, even with a token from this repo. GitHub creates an unprotected "dev" Environment automatically the first time a workflow references one; add required-reviewer/branch-restriction protection rules yourself in Settings > Environments whenever you're ready — nothing in this repo configures that automatically.

## What's actually been verified

- `tofu validate` passes for `bootstrap` and `environments/dev` (including the ECR, VPC, ALB, ECS-service, edge modules, and the T5-07 KMS/Secrets Manager/task-policy/audit resources) — this succeeds even though `domain_name` has no default, since `validate` checks syntax/type consistency only, not that every variable has a value.
- `tofu plan` for `bootstrap` was run for real against a live AWS account (`414987372853`) and produced a genuine, correct plan (`7 to add, 0 to change, 0 to destroy` — the state bucket, lock table, and the GitHub OIDC provider) — read-only, created nothing.
- `tofu plan` for `environments/dev` requires the bootstrap to actually be applied first (the S3 backend it points to doesn't exist until then) — this is the expected chicken-and-egg dependency, not a bug, and applies equally to every resource added to this environment since (ECR, VPC, ALBs, ECS, edge, KMS/Secrets/audit). `tofu init -backend=false` was used instead to verify the module wiring resolves correctly.
- `terraform-plan.yml` and `image-scan.yml` both pass `actionlint` with zero findings.

**Nothing has been applied.** No AWS resources and no GitHub repository settings exist because of this directory/workflow yet — that was an explicit scope decision, not an oversight. Run the bootstrap for real, then set the three repository variables above, when you're ready to actually stand this up.

## VPC/ALB/ECS baseline (T5-05)

- One VPC (`10.0.0.0/16`), three AZs, one public + one private subnet per AZ. A single NAT gateway (not one per AZ) is a deliberate cost/availability tradeoff for a self-funded MVP — see the comment in `modules/vpc/main.tf` for the residual risk this leaves.
- Two ALBs (ADR-012): `public_alb` (internet-facing, 0.0.0.0/0 on the security group) fronts `creation`+`redirect`; `internal_alb` (no public IP/DNS, security group scoped to the VPC's own CIDR only) fronts `admin`. This is the actual "no public bypass" mechanism (NFR-SEC-004) — not `cmd/admin`'s loopback-bind default, which only matters for local/scaffold runs.
- Both ALBs are **HTTP-only** right now — TLS/ACM/CloudFront is `T5-06`'s scope, not built here. Treat the public ALB as directly internet-reachable over plain HTTP until then; not a final production edge posture.
- Four ECS/Fargate services, one per deployment unit, images sourced from the `T5-04` ECR repositories (`<repo>:latest` — no real image has ever been pushed there, since ECR itself has never been applied). `worker` gets no ALB attachment at all (no HTTP surface by design).
- Each service gets its own ECS execution role (pull image, write logs) and an **empty** task role — real permissions (DynamoDB, ElastiCache/Valkey, KMS) are `T5-07`'s scope, added additively once known.
- No DynamoDB or ElastiCache/Valkey resources are provisioned by any current task — the running application code's `DYNAMODB_ENDPOINT`/`VALKEY_ADDR` env vars are not set here and would fall back to their `localhost` defaults, which are wrong in a real deployment. This is a known gap in the task breakdown, not silently patched.

## Edge: Route 53/ACM/CloudFront/WAF (T5-06)

- `module.edge` (`modules/edge/`) creates a Route 53 hosted zone, a DNS-validated ACM certificate, a CloudFront distribution fronting the public ALB, and a WAFv2 Web ACL — all keyed off `var.domain_name`, which has **no default** (see above).
- **Caching is disabled** on the distribution (AWS managed "CachingDisabled" policy) for every response — a redirect can transition Active → Suspended and must reflect that within the 60-second target (`docs/decisions/DEC-008.md`); an independent CDN TTL would be a second, uncoordinated staleness source on top of the already-coordinated cache/invalidation design in `ARC-007`. See the comment in `modules/edge/main.tf` before changing this.
- **CloudFront preserves path case by default** (`docs/decisions/DEC-010.md`'s case-preservation requirement) — nothing in this configuration rewrites, normalizes, or case-folds the request path; WAF rule evaluation may normalize a copy of the path internally for pattern matching, but never rewrites what's actually forwarded to the origin.
- The two generic WAF managed rule groups (`AWSManagedRulesCommonRuleSet`, `AWSManagedRulesKnownBadInputsRuleSet`) start in **COUNT** (observe-only) mode — this project has no penetration-test or false-positive evidence yet (`docs/SRS.md` G-007) to justify BLOCK. The per-IP rate-based rule does actively block (that's its purpose).
- **CloudFront-to-origin is plain HTTP**, not HTTPS — the public ALB has no HTTPS listener yet. The public-internet-facing hop (viewer → CloudFront) is full TLS; only the CloudFront → ALB hop over the AWS backbone is unencrypted. Closing this needs a second, regional ACM certificate and an ALB HTTPS listener — not built here.
- If this is ever applied, `module.edge`'s `name_servers` output must be set as the domain's NS records at whatever registrar the domain is bought through — Route 53 is not authoritative for the domain until that happens, regardless of anything else here.

## KMS / Secrets Manager / task roles / audit (T5-07)

- **Real least-privilege task-role policies** for `creation` and `redirect`, replacing T5-05's empty placeholders — grounded in the actual DynamoDB calls each service's Go code makes (`internal/mapping`, `internal/keyalloc`), not a blanket policy. See the comment in `task-policies.tf` for the exact reasoning per table/action. `admin` and `worker` keep empty task roles — nothing in their scaffold code exercises any AWS permission yet.
- **`dynamodb:CreateTable` is deliberately not granted**, even though `EnsureTables` in both `internal/mapping` and `internal/keyalloc` calls it on startup. A compromised task creating arbitrary tables is a real blast-radius risk; table provisioning belongs in IaC. This means `EnsureTables` would fail with `AccessDenied` against real AWS today — an honest gap, not silently patched, and it compounds the already-flagged absence of any DynamoDB-provisioning task.
- **Three customer-managed KMS keys**, each for a narrow reason DEC-004's provider-managed-KMS default doesn't cover: `secrets` (per-secret access control the account-wide default Secrets Manager key can't express), `image_signing` (asymmetric, sign-only — closes the gap `docs/poc/T5-04-images-ecr.md` flagged: "a real deployment would use a managed KMS-backed signing identity ... not a static keypair"), `cloudtrail` (log-integrity encryption with an explicit CloudTrail-service-trusting key policy).
- **One Secrets Manager secret** (`url-shortener/dev/valkey-auth-token`) created as an **empty container only** — no version, no fabricated value — since no ElastiCache/Valkey resource exists yet to have a real AUTH token. Demonstrates the mechanism (KMS-encrypted, IAM grant scoped to exactly this one secret ARN, on `creation`+`redirect` only) without inventing fake secret content.
- **A new GitHub Actions OIDC role** (`image_signing_role`, via the same `github-actions-role` module T5-03 established) trusted only for `repo:NoIr143/url-shortener:ref:refs/heads/main`, granted `kms:Sign`/`kms:GetPublicKey`/`kms:DescribeKey` on the `image_signing` key only. **No workflow uses this role yet** — deciding when signing should happen and wiring a workflow to call `cosign sign --key awskms:///<key-id>` with it is deliberate follow-up work.
- **CloudTrail** (multi-region, log-file validation, KMS-encrypted, dedicated S3 bucket with a TLS-only + CloudTrail-service-only bucket policy), **GuardDuty** (detector enabled), **Security Hub** (account enabled, AWS Foundational Security Best Practices standard subscribed). **AWS Config is deliberately not built** — it needs a recorder, delivery channel, its own IAM role, and a chosen rule set, and no compliance rules have been decided for this project; adding it now would be scope growth disproportionate to this task.
- **No live negative-access test was run** — that requires `apply` plus either a real unauthorized `AssumeRole`/API call attempt or the AWS IAM Policy Simulator against a real role ARN, neither of which exist under write-only scope. Every claim above is a structural/policy-document review, not a live test — same limitation this session has flagged for every prior AWS-touching task.

## Region

No region has been formally decided anywhere in this project (`docs/decisions/DEC-003.md` confirms only "single-region", not which one). `us-east-1` is used as a placeholder default throughout — change the `region` variable before applying for real if that's not where you want this to live.
