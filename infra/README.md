# Infrastructure (OpenTofu)

Per `ADR-015`: OpenTofu, remote encrypted state, no long-lived CI keys. This directory currently covers **T5-02 through T5-05's scope** — the remote-state backend, root environment scaffold, GitHub Actions OIDC trust, ECR repositories, and a VPC/ALB/ECS-Fargate baseline. It does not yet provision the edge (Route 53/ACM/CloudFront/WAF) or the remaining security services (`T5-06`/`T5-07`); those add resources into `environments/dev` (and later a `prod`, if one is ever funded) as they land.

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
                               repositories, a three-AZ VPC, public+internal ALBs, and an
                               ECS/Fargate cluster with one service per deployment unit.
  modules/
    tags/                      Standard tag map, shared by every environment.
    github-actions-role/       Reusable OIDC-trust IAM role for one workflow/environment.
    ecr-repository/            Reusable ECR repository (immutable tags, scan-on-push).
    vpc/                       Three-AZ VPC: public+private subnets, IGW, single NAT GW.
    alb/                       Reusable ALB (internet-facing or internal), HTTP-only for now.
    ecs-service/               Reusable Fargate service: task def, execution/task IAM
                               roles, security group, optional ALB target group/rule.

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

- `tofu validate` passes for `bootstrap` and `environments/dev` (including the ECR, VPC, ALB, and ECS-service modules).
- `tofu plan` for `bootstrap` was run for real against a live AWS account (`414987372853`) and produced a genuine, correct plan (`7 to add, 0 to change, 0 to destroy` — the state bucket, lock table, and the GitHub OIDC provider) — read-only, created nothing.
- `tofu plan` for `environments/dev` requires the bootstrap to actually be applied first (the S3 backend it points to doesn't exist until then) — this is the expected chicken-and-egg dependency, not a bug, and applies equally to every resource added to this environment since (ECR, VPC, ALBs, ECS). `tofu init -backend=false` was used instead to verify the module wiring resolves correctly.
- `terraform-plan.yml` and `image-scan.yml` both pass `actionlint` with zero findings.

**Nothing has been applied.** No AWS resources and no GitHub repository settings exist because of this directory/workflow yet — that was an explicit scope decision, not an oversight. Run the bootstrap for real, then set the three repository variables above, when you're ready to actually stand this up.

## VPC/ALB/ECS baseline (T5-05)

- One VPC (`10.0.0.0/16`), three AZs, one public + one private subnet per AZ. A single NAT gateway (not one per AZ) is a deliberate cost/availability tradeoff for a self-funded MVP — see the comment in `modules/vpc/main.tf` for the residual risk this leaves.
- Two ALBs (ADR-012): `public_alb` (internet-facing, 0.0.0.0/0 on the security group) fronts `creation`+`redirect`; `internal_alb` (no public IP/DNS, security group scoped to the VPC's own CIDR only) fronts `admin`. This is the actual "no public bypass" mechanism (NFR-SEC-004) — not `cmd/admin`'s loopback-bind default, which only matters for local/scaffold runs.
- Both ALBs are **HTTP-only** right now — TLS/ACM/CloudFront is `T5-06`'s scope, not built here. Treat the public ALB as directly internet-reachable over plain HTTP until then; not a final production edge posture.
- Four ECS/Fargate services, one per deployment unit, images sourced from the `T5-04` ECR repositories (`<repo>:latest` — no real image has ever been pushed there, since ECR itself has never been applied). `worker` gets no ALB attachment at all (no HTTP surface by design).
- Each service gets its own ECS execution role (pull image, write logs) and an **empty** task role — real permissions (DynamoDB, ElastiCache/Valkey, KMS) are `T5-07`'s scope, added additively once known.
- No DynamoDB or ElastiCache/Valkey resources are provisioned by any current task — the running application code's `DYNAMODB_ENDPOINT`/`VALKEY_ADDR` env vars are not set here and would fall back to their `localhost` defaults, which are wrong in a real deployment. This is a known gap in the task breakdown, not silently patched.

## Region

No region has been formally decided anywhere in this project (`docs/decisions/DEC-003.md` confirms only "single-region", not which one). `us-east-1` is used as a placeholder default throughout — change the `region` variable before applying for real if that's not where you want this to live.
