---
description: Print the full context of a cloud-skeleton pitfall. Pass the pitfall number as $ARGUMENTS (1–20).
---

Explain cloud-skeleton pitfall **#$ARGUMENTS** in detail.

Steps:

1. Read `.ralph/specs/stdlib/skeleton-conventions.md` (or, if running in the impl repo, `docs/ARCHITECTURE.md` § "Pitfalls → mitigations").

2. Find the entry for pitfall #$ARGUMENTS.

3. Print:

```
Pitfall #<n> — <short title>

Past failure
  <one paragraph: what broke, when, why>

Mitigation in this codebase
  <one paragraph: where the fix lives — file paths, function names — and how it works>

How to keep it mitigated
  <bulleted list of dos and don'ts>

Code references
  <file paths in the impl repo where the mitigation is enforced>
```

4. If the pitfall number is out of range (not 1–20), say so and offer to list all 20 with one-line summaries.

This command exists because Ralph and the human operator both need to look up pitfall details mid-implementation. Saving the click-through to the doc file by ~30 seconds per use, several times per iteration, is worth the slash command.

Be terse. The doc has the full context; quote the relevant snippet, don't paraphrase.
