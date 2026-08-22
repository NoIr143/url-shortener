# Infrastructure (OpenTofu)

Per `ADR-015`: OpenTofu, remote encrypted state, no long-lived CI keys. This directory currently covers **T5-02 and T5-03's scope** — the remote-state backend, root environment scaffold, and GitHub Actions OIDC trust. It does not yet provision the VPC, compute, edge, or security services (`T5-05`/`T5-06`/`T5-07`); those add resources into `environments/dev` (and later a `prod`, if one is ever funded) as they land.

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
                               Includes the dev-scoped GitHub Actions IAM role.
  modules/
    tags/                      Standard tag map, shared by every environment.
    github-actions-role/       Reusable OIDC-trust IAM role for one workflow/environment.

.github/workflows/
  terraform-plan.yml           Runs `tofu plan` for infra/environments/dev on PRs that
                               touch infra/, authenticated via OIDC — no AWS key stored
                               in GitHub.
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

- `tofu validate` passes for `bootstrap` and `environments/dev`.
- `tofu plan` for `bootstrap` was run for real against a live AWS account (`414987372853`) and produced a genuine, correct plan (`7 to add, 0 to change, 0 to destroy` — the state bucket, lock table, and the GitHub OIDC provider) — read-only, created nothing.
- `tofu plan` for `environments/dev` requires the bootstrap to actually be applied first (the S3 backend it points to doesn't exist until then) — this is the expected chicken-and-egg dependency, not a bug. `tofu init -backend=false` was used instead to verify the module wiring (`environments/dev` → `modules/tags`, `modules/github-actions-role`) resolves correctly.
- `terraform-plan.yml` passes `actionlint` with zero findings.

**Nothing has been applied.** No AWS resources and no GitHub repository settings exist because of this directory/workflow yet — that was an explicit scope decision, not an oversight. Run the bootstrap for real, then set the three repository variables above, when you're ready to actually stand this up.

## Region

No region has been formally decided anywhere in this project (`docs/decisions/DEC-003.md` confirms only "single-region", not which one). `us-east-1` is used as a placeholder default throughout — change the `region` variable before applying for real if that's not where you want this to live.
