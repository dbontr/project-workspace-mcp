---
name: project-stewardship
description: Repository-owned operating standard for Project Workspace MCP. Use for MCP project or standalone task workflows, provider-aware GitHub and local Git source control, optional Git-backed project knowledge and its record format, Issues/PRs/Projects planning, distributed Kubernetes workspaces, hardware leases, external resource boundaries, artifacts, research provenance, professional implementation, release verification and durable handoffs. Compose with the general professional-code skill for software quality when available.
---

# Project Stewardship

Treat durable source, knowledge, work tracking, compute and artifacts as **distinct authorities**. Select each deliberately. A successful task leaves a verifiable technical result and only the records the project policy authorizes.

## Operating procedure

1. **Discover the contract.** Inspect the connected Project Workspace MCP endpoint, authenticated caller, live `tools/list` schemas, repository's instructions, current branch and `REFERENCES.md`, provider support, project roles, cluster inventory and existing repository links. The presence of this repository-owned skill grants no tool access or credential. Load the relevant reference for the actual operation.
2. **Classify the work.** A standalone task may use authorized compute and explicitly saved task files but creates no project or durable project knowledge. A project owns one or more canonical Git source repositories. Knowledge is optional (`none` by default; `git` only after explicit configuration). Planning and large-object storage are independently chosen. Never create a project or knowledge repository as a convenience workaround.
3. **Resolve the four authorities.** Record the selected `source` Git remote(s), optional `knowledge` Git remote/path, optional `planning` provider/board, and durable artifact store. A code and knowledge repository may be the same repository. Project boundaries may span multiple repos; repository history is never copied into the controller as a second source of truth.
4. **Plan at the right level.** Use provider-supported Issues for bounded work and a board only when its views or workflow add value. Use Git for technical evidence, decisions and code. Preserve links between work items, commits, experiments and verified artifacts without mirroring the same mutable status in several places.
5. **Implement to the repository standard.** Apply `professional-code` when available; otherwise follow its durable principles: repair the root cause at the canonical ownership boundary, reuse established mechanisms, maintain root `REFERENCES.md`, run proportionate tests and inspect the final diff. Remove conversational or AI-development residue, generic boilerplate, redundant compatibility paths and unnecessary assistant attribution, but preserve legally or contractually required provenance.
6. **Execute with explicit resources.** Use `references/mcp-operations.md` for MCP workflows and live schemas for exact arguments. Confirm the worker, namespace, approved image, resource claim and hardware ownership before queueing any operation. Keep external machines, remote clusters and USB/FPGA boards outside the general pool until explicitly authorized and independently confirmed.
7. **Publish and verify.** Resolve the expected Git head, commit to the selected authority, verify the new head and files, and use the provider's actual review mechanism when available. Inspect test/build output and artifacts; do not equate a queued operation, passing mock or open PR with accepted production behavior. Verify cleanup and release of hardware.
8. **Write knowledge only when enabled.** Inspect `knowledge_policy`. With mode `none`, do not generate durable project notes, decisions or handoffs. With explicit mode `git`, use `knowledge_configure`, `knowledge_git_read` and revision-guarded `knowledge_git_publish` under the chosen repository, branch and prefix. Follow `references/knowledge-standard.md` for evidence, decisions, experiments and handoffs. Verify the Git commit by read-back; never fall back to SQLite or create a shadow store.
9. **Close the loop.** Reconcile the issue/PR/board status only with observed evidence. Report the exact commit, work item, tests run, resources used, released leases, durable output hashes and outstanding blockers. A handoff is concise state for the next operator, not a transcript.

## Provider and GitHub boundary

- **GitHub source:** use authorized GitHub repository tools for content and commits; use GitHub Issues, pull requests and Projects V2 only if the account and repository actually expose them.
- **Local bare Git source:** use `local:OWNER/REPOSITORY` only if the controller's local Git storage is configured. Exact-commit reads and guarded publication do **not** imply Issues, PRs or Projects. Treat those as unavailable unless a separate provider is selected.
- **Hybrid:** source and knowledge may be local Git while an independent GitHub repository is linked with role `planning`. Track Issues, PRs and a board against that **actual GitHub repository**; do not claim they modify the local source repository. Link local commits and evidence by immutable identifiers or verified URLs.
- **GitLab, Gitea or generic remote Git:** inspect live adapter capabilities; unsupported transports or provider APIs are not assumed operational. Surface the missing capability and retain the working Git operations without fabricated substitutes.
- On every provider, obey branch protections, required reviews, credential scopes and explicit confirmations. Do not self-approve, bypass review requirements, force-push, rewrite unrelated histories or modify account-wide repositories without authority.

Read `references/github-workflow.md` for Issues, Projects V2, branches, PRs, review and provider fallbacks. Read `references/mcp-operations.md` for provider-linked Git tool selection and exact controller workflows.

## Knowledge and documentation

Use the canonical, human-readable layout in `references/knowledge-standard.md`. Prefer one source of truth for each claim: source code in Git; project status in the canonical work tracker; decisions and evidence in versioned knowledge; large binary evidence in the designated artifact store with a digest-linked manifest. Existing repositories may use equivalent conventions; adapt deliberately instead of making competing hierarchies.

Read `references/knowledge-standard.md` before creating or changing a knowledge repository. An independent task may retain explicitly saved task files without enabling project knowledge. Use `go run ./skills/project-stewardship/scripts/check_knowledge.go --strict ./knowledge` from a local repository checkout when the standard layout is in use. The Go validator checks structure, not the truth of research evidence.

## Cluster and delivery discipline

Read `references/mcp-operations.md` for project workspaces, finite tasks, task claims, provider access, artifact transfer, catalog administration and recovery. Read `references/cluster-and-delivery.md` when allocating compute, moving files, building OCI images, deploying software, running FPGA/EDA work, recovering devices or declaring acceptance. Compute is an ephemeral execution plane. Neither a Pod filesystem nor an unpushed clone is authoritative source or knowledge. Track image identities, resource requests, exact source commits and artifact digests for reproducibility; avoid copying credentials into jobs or logs.

## Scope and limits

This is the single MCP-specific skill maintained with the Project Workspace MCP source. `references/mcp-operations.md` covers execution and storage operations; `references/github-workflow.md` covers provider-specific planning; `references/knowledge-standard.md` covers opt-in Git persistence; `references/cluster-and-delivery.md` covers compute and release assurance. The independent, general-purpose `professional-code` skill may supply deeper software review and repository-quality rules, but is not part of this repository and does not grant authorization.

The skill does not install a ChatGPT connector, grant credentials, establish hardware mappings, add an external cluster adapter, guarantee remote durability or confer permission to merge. Where a live tool or provider feature is unavailable, complete only the authorized supported portion and report the gap. Never silently weaken invariants.
