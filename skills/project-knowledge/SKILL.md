---
name: project-knowledge
description: Optional Git-backed project knowledge for Project Workspace. Use only when a user explicitly enables persistent project notes, research evidence, decisions, or handoffs. Read and write versioned Markdown and source references through a user-selected Git remote, including GitHub, GitLab, or self-hosted Git. When disabled, store no intentional project knowledge. This skill is independent of the mandatory Git source-code workflow and optional for ordinary compute.
---

# Project Knowledge

Use this skill only when the project owner chooses durable knowledge. Project Workspace requires Git for authoritative code, but does not require project knowledge. A user may put knowledge in an existing code repository or in a separate Git repository; never create an additional repository automatically. A Git host is a transport and authorization choice, not a different skill.

## Storage policy

1. **Disabled (`none`).** Do not save, update, index, or automatically synthesize persistent knowledge. Do not call controller-local project note or contribution writes as a substitute. Source code still belongs to the project's configured Git remote; operational controller state (jobs, worker pairing, artifacts, leases) is not a knowledge store.
2. **Enabled (`git`).** Determine the explicitly approved Git remote, repository, branch, and knowledge path before writing. The remote may be on GitHub, GitLab, Gitea, another authenticated server, or a locally hosted Git service. A Git repository hosted on the same machine is still Git, but not off-host disaster recovery.
3. The code repository and the knowledge repository may be identical, but they have separate file paths, permissions and retention decisions. Default to a folder within an existing authorized repository when the user prefers no additional repositories.
4. Git is authoritative for knowledge in this mode. SQLite, a worker workspace, the model's memory, a local clone, a library index or an MCP contribution receipt is not proof of a successful remote Git commit. A non-Git provider can store attachments or cached indexes only when explicitly authorized, and its references must be versioned in Git.

## Capabilities and authorization

- Inspect the connected MCP tools and selected provider. The controller exposes `knowledge_policy`, `knowledge_configure`, `knowledge_git_read`, and `knowledge_git_publish` for explicitly selected GitHub or operator-mounted bare Git storage. `knowledge_git_publish` uses a verified branch head and remote read-back; GitHub requires a user-owned PAT, while `local:OWNER/REPOSITORY` requires a configured approved local Git root and author identity. Generic GitLab or Gitea writes still require a tested remote adapter.
- Resolve project ownership and a configured repository before reading or writing. Require a credential scoped to the selected repository; keep it in controller-managed secrets and never pass it to a worker Pod, file, note, output log, or shell command payload.
- Prefer `git_repository_head`, `git_read_file`, and revision-guarded `git_publish_file` when the repository is linked to a project. GitHub-specific Issues, Pull Requests, and Projects are optional enhancements, not Git protocol requirements. Use `git_provider_capabilities` to inspect whether they are available before invoking them.
- Reject unknown remote hosts, redirects, credential-in-URL inputs, local filesystem escape paths, Git command-option injection and unreviewed remote hooks. Do not weaken network or credential restrictions merely to make a remote accessible.

## Explicit policy

A project starts with knowledge mode `none`. Use `knowledge_configure` with
the expected policy version to enable `git` and select an authorized GitHub or local bare Git repository,
branch and directory. Do not invoke local notes or contributions as a
substitute for remote publication. Publish a document relative to the
configured directory with `knowledge_git_publish`, supplying the expected
40-character branch head. Verify its returned commit SHA and published
content digest. A project-free task cannot enable this policy; an independent
task's files and results are not promoted into project knowledge automatically.

## Standard knowledge layout

When `project-stewardship` is installed, follow its `references/knowledge-standard.md` for record types, frontmatter, status vocabulary, evidence sections, experiments and concise handoffs. The Git repository remains authoritative; use that standard only with an explicitly enabled knowledge policy. For projects not using the companion skill, follow the layout below and preserve existing compatible conventions.

Use a conventional, explicitly configured repository subdirectory (for example `knowledge/`). Keep source trees and build outputs outside it.

- `Project.md` — purpose, scope, assumptions and principal research questions.
- `Context.md` — current state and outstanding work; replace only using an expected Git revision.
- `decisions/` — dated decisions with evidence, alternatives and uncertainties.
- `findings/` — empirical results, citations, reproducibility details and limitations.
- `handoffs/` — concise state transitions, exact commit IDs, verified artifacts and next actions.
- `sources/` — references to immutable source revisions, locators and checksums. Use Git LFS for large originals only if the chosen server supports it and the user authorizes it.

The exact layout may follow existing repository conventions; do not add empty folders, redundant templates or a competing local database.

## Durable write procedure

1. Read the selected remote branch and its exact commit SHA. Fetch the target knowledge paths and distinguish verified data from proposals. Do not infer content from an old checkout.
2. Prepare the minimum necessary Markdown changes. Record source commit IDs, exact citations or artifact checksums when evidence is used. Never fabricate page numbers, hardware test results, or claims of successful deployment.
3. Publish a Git commit through the authorized provider with an expected base SHA, on a reviewed branch where applicable. A concurrent update requires a fetch and merge/rebase review; never force-push to resolve a knowledge conflict.
4. Read back the remote branch and confirm the intended commit and file contents. Verify that the credential was not embedded in the commit. Report a rejected push as a failure, not as a saved knowledge entry.
5. If the remote is unavailable, fail explicitly. Do not silently write to controller-local SQLite or promise later synchronization. The user may retry or intentionally change storage policy.

## Research evidence and handoffs

- Register an original document by an immutable Git revision or a verified SHA-256 source object. For external attachments, write a stable reference and expected digest into Git; do not treat a mutable URL as immutable evidence.
- When supported by the selected backend, index passages with genuine page, section or line locators. Read a passage before quoting and distinguish lexical from semantic search. An index may be local and rebuildable, but the canonical text and provenance remain in Git.
- For a handoff, preserve the project objective, decisions, exact source revision, verified output hashes, test outcomes, remaining blockers and next action. Distinguish staged/reviewed software from deployed and independently recovered software.
- Use different branches or independent contribution files for concurrent writers. Apply an expected revision when consolidating Context.md. Protect confidential research, secrets, licensed materials and personal data according to the selected Git host's access and retention policy.

## Relationship to Project Workspace

Use the separate `project-workspace` skill for Kubernetes workspaces, toolchain images, source-code Git operations, hardware resources and executable artifacts. This skill is not required for those operations and does not change the code repository. A project with knowledge mode `none` must not generate knowledge commits. A project with knowledge mode `git` writes durable knowledge only after verifying a commit in its selected Git remote.

## Recovery

Verify Git remote backups and access independently of the compute cluster. For self-hosted Git, test bare-repository restoration and required Git LFS objects where used. A GitHub or GitLab remote provides remote persistence but does not by itself establish a separate verified organizational backup. Never delete the old knowledge source until the new Git repository and any referenced objects can be restored.
