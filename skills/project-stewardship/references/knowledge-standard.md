# Versioned Project Knowledge Standard

This convention applies **only** when the project owner has selected `knowledge=git`. Code remains in its chosen Git source repository even when knowledge is disabled. Use the established layout of an existing repository when equivalent; avoid redundant migrations or empty directories.

## Authority and location

- Choose the approved Git repository, branch and knowledge prefix explicitly. A project may use `source=github` with `knowledge=local-git`, both on GitHub, or both in one local bare Git repository. Verify provider access before first publication.
- Use Markdown in one configured prefix (typically `knowledge/`). Do not create a second database of canonical decisions. SQLite can hold operational indexes and cached search state, never the authoritative project knowledge in this mode.
- For concurrent authors, commit on independent branches or independent record files, then merge/reconcile against the verified latest head. Never force-write a stale `Context.md`.
- Store large datasets, images, bitstreams and model weights in an authorized artifact/object store. A Git record must include the immutable artifact ID, SHA-256, size and location; use Git LFS only when the provider supports and the user approves it. Confirm restore ability before removing originals.

## Canonical layout

```text
knowledge/
  Project.md                 # project charter, durable scope and success criteria
  Context.md                 # current verified state and immediate next action
  decisions/                 # technical choices and tradeoffs
    2026-11-05-storage-authority.md
  findings/                  # claims with reproducible evidence
    2026-11-05-dma-bandwidth.md
  experiments/               # methods and measurements
    2026-11-05-transfer-test.md
  handoffs/                  # concise continuation records
    2026-11-05-checkout-review.md
  sources/                   # immutable provenance and artifact manifests
    sample-dataset.md
```

Create only folders and files justified by actual records. One `Project.md` and one `Context.md` per knowledge scope, not per chat or worker. The parent repository's `README.md` explains the software; root `REFERENCES.md` lists authoritative external technical sources. `knowledge/sources` tracks specific research evidence and immutable project input identities. These purposes are distinct.

### Project.md

Use these headings: `# Project`, `## Purpose`, `## Scope`, `## Acceptance criteria`, `## Authorities`. Under `Authorities`, identify linked code repositories, the configured knowledge repository/ref, planning provider if present and binary artifact store. Do not embed tokens or internal-only credentials. The charter should be stable, not an activity log.

### Context.md

Use `# Context`, `## Verified state`, `## In progress`, `## Blockers`, `## Next action`, `## References`. Link exact commit SHAs, issue or PR identifiers and artifact digests. Distinguish provider-reported state from independently verified behavior. Keep this file concise enough to orient an operator; do not paste transcript, scratchpad, entire test logs or guessed hardware state. A stale context file must be corrected by reviewing the latest remote head, not by overwriting it blindly.

## Record syntax

Use UTF-8 Markdown with this frontmatter on each record in `decisions/`, `findings/`, `experiments/`, `handoffs/` and `sources/`:

```yaml
---
id: dma-transfer-20261105
kind: experiment
status: provisional
date: 2026-11-05
---
```

`id` is a unique, stable, lowercase hyphenated identifier across the knowledge scope. `kind` must match the directory: `decision`, `finding`, `experiment`, `handoff` or `source`. `status` is one of `proposed`, `provisional`, `verified`, `superseded`, `archived`. `date` is the original ISO 8601 calendar date of the record; do not change it when editing. Optional plain-string fields include `owner`, `updated` (ISO date), `supersedes` (another record ID) and `issue` (provider issue URL). For more complex provenance, use normal Markdown tables or fenced JSON in the body rather than nested ad hoc frontmatter.

A `verified` finding requires evidence and a repeatable method. A completed experiment is not necessarily a `verified` scientific conclusion. A decision's status describes acceptance of the decision, not experimental certainty. When a conclusion is superseded, retain the old record with `status: superseded` and link its successor; never erase contradictory evidence merely to simplify the narrative.

## Required record sections

| Directory | Markdown heading | Required sections |
| --- | --- | --- |
| `decisions/` | `# Decision: ...` | `## Context`, `## Decision`, `## Alternatives`, `## Consequences`, `## Evidence` |
| `findings/` | `# Finding: ...` | `## Claim`, `## Method`, `## Evidence`, `## Limitations` |
| `experiments/` | `# Experiment: ...` | `## Question`, `## Reproduction`, `## Results`, `## Interpretation`, `## Artifacts` |
| `handoffs/` | `# Handoff: ...` | `## Current state`, `## Verified`, `## Blockers`, `## Next actions` |
| `sources/` | `# Source: ...` | `## Identity`, `## Locator`, `## Integrity`, `## Usage` |

For evidence, record which claims are **observed**, which are **derived**, and which are **hypotheses**. Include units, sample sizes and uncertainty where relevant. A source reference should contain a DOI, URL, repository commit SHA or artifact ID, an access date when appropriate, plus a SHA-256 digest for preserved binaries. Do not fabricate citations, page numbers, experiment results, tests or provenance.

### Experiment reproduction block

A reproducible test should identify, when applicable, the exact source commit, versioned OCI image (prefer digest), entrypoint/command, input artifact SHAs, software/driver versions, node or hardware device model, allocation type, environment variables that affect the result (never their secret values), start/stop condition, observed outputs and failure modes.

Example:

```markdown
## Reproduction

- Source: `owner/repo@0123456789abcdef0123456789abcdef01234567`
- Image: `registry.example/research@sha256:<verified-digest>`
- Hardware: GPU device lease `node/device`, verified by scheduler
- Input: artifact `dataset-id`, SHA-256 `<verified-digest>`
- Command: `./tests/transfer --iterations 100`
- Acceptance: independently check the exact output hash and measured units
```

Angle brackets in the sample are placeholders, not actual evidence. Replace them with verified data before committing a record.

## Publication and migration

1. Inspect `knowledge_policy`; do nothing when mode is `none`.
2. Read the latest approved knowledge branch and the target file. Protect concurrent edits with its expected head SHA.
3. Write a complete record or carefully update `Context.md`; check its citations and source links. Run the structural checker against a local checkout when available.
4. Publish to Git. Read back the exact commit and file content and compare their hashes. Report the new immutable revision.
5. For migrations, read old controller-local notes, map records into this layout with their real provenance, verify the Git commit and restore ability, then retire legacy writes. Do not silently reinterpret operational cache entries as accepted findings.

## Access control and independent recovery

- `knowledge_policy` begins at `none`. Only the project owner may approve a change to `git`; use `knowledge_configure` with the expected policy version, explicit repository, branch and prefix. Configuration is separate from publication and must not create records automatically.
- Verify the connected provider's repository permission before a write. GitHub requires a scoped authorized credential; `local:OWNER/REPOSITORY` requires an operator-approved bare Git root and defined author identity. Do not expose controller credentials to workspaces, notebooks, Git records, logs or external resource adapters.
- For `knowledge_git_publish`, supply the expected branch head and verify the returned commit ID, then independently read back the document. A revision conflict requires examining both versions and an intentional merge. Never overwrite a concurrent author's `Context.md` or force-push to resolve it.
- A locally hosted bare Git repository is not an independent off-host backup. Periodically verify both repository restoration and any Git LFS or external artifact objects it references. GitHub or GitLab hosting does not itself prove an independent organizational backup. Preserve the original knowledge source until the chosen destination and referenced objects pass an actual restore check.
- Treat indexed passages, search caches, workspace snapshots and operational task files as auxiliary records. Unless the project specifically authorizes their durable publication in the configured Git repository, they are not canonical project knowledge. Maintain exact source identity and revision for reproducible claims.
- When knowledge is `none`, do not populate this layout as a side effect of task completion or cluster operation. A user-requested portable handoff may still be returned in the current conversation without creating permanent project knowledge.

## Example: decision

```markdown
---
id: source-authority-20261105
kind: decision
status: verified
date: 2026-11-05
---
# Decision: Source authority
## Context
The controller executes temporary workspaces; canonical source must survive their deletion.
## Decision
Keep source code in the chosen Git remote. Keep knowledge optional.
## Alternatives
Preserving authoritative source in a worker volume was rejected because it couples durability to workload life.
## Consequences
All release claims must identify and verify a reachable Git commit.
## Evidence
The repository's verified branch head and publication response are the record of source identity.
```
