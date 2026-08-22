# Infrastructure (OpenTofu)

Per `ADR-015`: OpenTofu, remote encrypted state, no long-lived CI keys. This directory currently covers **T5-02's scope only** — the remote-state backend and root environment scaffold. It does not yet provision the VPC, compute, edge, or security services (`T5-05`/`T5-06`/`T5-07`); those add resources into `environments/dev` (and later a `prod`, if one is ever funded) as they land.

## Layout

```
infra/
  bootstrap/            One-time: creates the S3 state bucket + DynamoDB lock table.
                         Uses LOCAL state (chicken-and-egg — nothing else exists yet
                         to point a remote backend at).
  environments/
    dev/                 Root module for the dev environment. Remote S3 backend.
  modules/
    tags/                Standard tag map, shared by every environment.
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

## What's actually been verified

- `tofu validate` passes for both `bootstrap` and `environments/dev`.
- `tofu plan` for `bootstrap` was run for real against a live AWS account (`414987372853`) and produced a genuine, correct plan (`6 to add, 0 to change, 0 to destroy`) — read-only, created nothing.
- `tofu plan` for `environments/dev` requires the bootstrap to actually be applied first (the S3 backend it points to doesn't exist until then) — this is the expected chicken-and-egg dependency, not a bug. `tofu init -backend=false` was used instead to verify the module wiring (`environments/dev` → `modules/tags`) resolves correctly.

**Nothing has been applied.** No AWS resources exist because of this directory yet — that was an explicit scope decision, not an oversight. Run the bootstrap for real when you're ready to actually stand up state.

## Region

No region has been formally decided anywhere in this project (`docs/decisions/DEC-003.md` confirms only "single-region", not which one). `us-east-1` is used as a placeholder default throughout — change the `region` variable before applying for real if that's not where you want this to live.
