---
description: Fire the right review agents for the current branch's diff. Pass the iteration number as $ARGUMENTS for context.
---

Run a multi-agent review of the work-in-progress on this branch before opening a PR.

Iteration: $ARGUMENTS (if empty, infer from `.ralph/fix_plan.md`).

Steps:

1. Establish what changed: `git diff origin/main...HEAD --name-only`. Group changes by area (terraform, lambdas, frontend, worker, prompts, docs).

2. Fire the relevant agents in parallel — one Agent tool call each, all in a single message:
   - **`skeleton-pitfall-reviewer`** — always, if any of `terraform/`, `aws-setup/`, `cloudflare/`, `lambdas/`, or `.github/workflows/` changed.
   - **`cost-idempotency-reviewer`** — if any `lambdas/<svc>/main.go` was added or substantially edited.
   - **`bedrock-prompt-reviewer`** — if any `lambdas/pkg/prompts/` or `lambdas/pkg/schemas/` changed.
   - **`quality-rules-enforcer`** — if any test fixture under `testdata/` for spec/website/email was added (so we lint the fixture for fake facts before it gets baked into snapshots).

3. Collect the verdicts. Print a short summary:
   - Each agent's PASS/FAIL.
   - List of FAIL items with file:line.
   - For each FAIL, suggest the smallest concrete fix.

4. If everything is PASS, suggest the conventional commit message + PR title and remind to update `.ralph/fix_plan.md`.

5. If anything is FAIL, do NOT proceed to commit. The implementing-Claude or the human operator addresses each FAIL first.

Be terse. The agents already produce structured output; pass it through. Do not re-explain what each agent does.
