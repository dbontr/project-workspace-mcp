---
name: project-knowledge
description: Preserve, retrieve, and organize project context, decisions, research evidence, source passages, and handoffs in an authorized Project Workspace controller or user-selected knowledge store. Use when the user explicitly wants durable project memory, research-source indexing, knowledge retrieval, a project handoff, or a canonical knowledge record. This skill is optional and independent of compute workflows. GitHub is a supported source or publication destination, never a prerequisite; ordinary GitHub coding and project execution do not require this skill.
---

# Project Knowledge

Manage durable knowledge only when requested or needed for a task that explicitly depends on it. A connected Project Workspace controller can store project notes, immutable contributions, research sources, and artifact references without any GitHub account, repository, or GitHub Project board. This skill does not install a service, choose an authoritative store, enroll a device, or grant access. Discover live tools and the user's permitted projects before any read or write.

## Resolve authority

1. Identify the project and authorized owner. Ask for one essential missing project identity only if it cannot be resolved from live tools or current context.
2. Determine the authority for each record: controller-local storage, an explicitly designated existing GitHub repository, or another connected store. Do not create or link a GitHub repository merely to hold knowledge. Do not silently migrate or copy records between authorities.
3. If Project Workspace MCP is connected, discover `list_projects`, `knowledge_context`, `read_project_note`, `write_project_note`, `knowledge_contribute`, `knowledge_contributions`, `knowledge_contribution`, `knowledge_search_contributions`, and available research/library operations. Use only tools actually exposed and authorized. When these are unavailable, use another explicitly authorized store or report the unavailable operation; do not invent MCP methods.
4. Keep repository source history, project planning, research documents, and compute artifacts distinct. A GitHub source repository may be linked for provenance without becoming the canonical knowledge repository. A project may have zero linked repositories.

## Work with project records

- **Read before editing.** Retrieve `knowledge_context` to find the project's current Project/Context notes, latest handoff, recent decisions, and verified repository references. If not exposed, use the corresponding permitted read and list tools. Do not treat absent records as proof of a blank or new project.
- **Canonical notes.** Use `read_project_note` and write only with the current `expected_version`. On a revision conflict, re-read and reconcile rather than overwriting a concurrent update. Represent conclusions, unresolved questions, assumptions, dates, and next actions distinctly.
- **Immutable contributions.** Use `knowledge_contribute` for independent findings, decisions, notes, and handoffs. Read back the contribution and verify its identity and stored digest before reporting persistence. Link a repository only with an exact approved commit, or an artifact with its verified project-owned ID; neither is required for a plain-text contribution.
- **Concurrency.** Keep simultaneous workers' findings as immutable contributions until an authorized writer merges them into a canonical note. Never use a worker's `/workspace` filesystem as the sole authoritative record.
- **Handoffs.** Record the current objective, verified accomplishments, test evidence, blockers, exact artifact/source revisions, and next steps. Distinguish running services from proposed code, local tests from on-hardware tests, and a successful operation from an enqueued one.
- **Confidentiality.** Never store controller tokens, GitHub PATs, keys, license files, personal secrets, unredacted infrastructure credentials, or raw private logs in knowledge records. Share only content within the current project's authorization scope.

## Research evidence and retrieval

1. Register an immutable original in a permitted shared library: either a content-addressed uploaded object with a verified SHA-256, or an existing linked Git source pinned to an exact commit. Neither form requires creating a new repository.
2. Call `research_register_source` only after verifying that original. Ingest text using `research_ingest_passages` with genuine document locators and the current `expected_revision`. Use `append` for additional evidence and `replace` for a new visible index epoch; neither means the older evidence was deleted.
3. Discover `research_topics`, `research_browse_topic`, `research_sources`, and `research_search` where available. Read an exact passage with `research_read_passage` before quoting or citing it. Preserve the original file or Git reference, passage ID, location, and integrity information.
4. Report a lexical search as lexical, not semantic search. Do not invent page, line, or section numbers, citations, consensus, or knowledge-graph edges. When source preprocessing is unavailable, retain the original and mark indexing incomplete.
5. If the user's knowledge is in another authorized system, follow that system's read/write and concurrency semantics. Never replace it with an automatic GitHub mirror.

## Optional GitHub workflow

Use GitHub only when the user chooses it as a source or durable publishing destination. Existing code repositories remain code repositories; do not generate dedicated `project-knowledge` repositories by default. If the user opts into Git-backed knowledge, establish the target repository and path, read its exact base commit, and publish via reviewed revision-guarded writes. Verify the resulting commit/PR and make the authority decision explicit. A successful GitHub commit does not substitute for the controller's own backup of notes, contributions, artifacts, and databases.

## Verify recovery

Before claiming knowledge is protected against host loss, verify an encrypted off-host backup containing the selected authoritative database and referenced artifact bytes. Rehearse an isolated restore, check SQLite integrity and file digests as applicable, and verify that essential records are readable through the restored service. Report a local-only backup as local-only. Do not delete legacy knowledge or broad backups before independent restoration succeeds.

## Collaboration with Project Workspace

Use the separate `project-workspace` skill for Kubernetes execution, resource planning, hardware leases, workspace commands, CI, and GitHub source publication. Use this `project-knowledge` skill only for intentional knowledge operations. Both skills may operate on the same authorized controller without imposing a GitHub requirement or duplicating the underlying storage. A user who wants only compute should not need to install or invoke this skill.