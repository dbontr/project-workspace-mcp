---
name: project-stewardship
description: Govern professional research and software projects that use Project Workspace MCP, GitHub or an approved Git repository, optional Git-backed project knowledge, and Kubernetes or external compute clusters. Use when starting, planning, implementing, reviewing, documenting, handing off, or releasing a project; organizing repositories and durable knowledge; deciding how GitHub Issues, pull requests and Projects relate to local Git; or coordinating multi-repository engineering and hardware work. Compose with project-workspace, project-knowledge and professional-code rather than replacing their execution or coding instructions.
---

# Project Stewardship

Treat durable source, knowledge, work tracking, compute and artifacts as **distinct authorities**. Select each deliberately. A successful task leaves a verifiable technical result and only the records the project policy authorizes.

## Operating procedure

1. **Discover the contract.** Inspect the active Project Workspace MCP tools, the repository's instructions, current branch and `REFERENCES.md`, connected providers, cluster inventory, and existing project links. Never infer a tool, credential, hardware lease or permission from a skill's presence. Read the relevant reference below only when that activity is required.
2. **Classify the work.** A standalone task may use authorized compute and explicitly saved task files but creates no project or durable project knowledge. A project owns one or more canonical Git source repositories. Knowledge is optional (`none` by default; `git` only after explicit configuration). Planning and large-object storage are independently chosen. Never create a project or knowledge repository as a convenience workaround.
3. **Resolve the four authorities.** Record the selected `source` Git remote(s), optional `knowledge` Git remote/path, optional `planning` provider/board, and durable artifact store. A code and knowledge repository may be the same repository. Project boundaries may span multiple repos; repository history is never copied into the controller as a second source of truth.
4. **Plan at the right level.** Use provider-supported Issues for bounded work and a board only when its views or workflow add value. Use Git for technical evidence, decisions and code. Preserve links between work items, commits, experiments and verified artifacts without mirroring the same mutable status in several places.
5. **Implement to the repository standard.** Apply the installed `professional-code` skill for coding. Identify the canonical ownership boundary, reuse existing mechanisms, preserve existing conventions, maintain root `REFERENCES.md`, test behavior and review the diff. Keep final artifacts free of assistant conversation residue, generic boilerplate and gratuitous attribution. Do not conceal provenance where law, license or policy requires disclosure.
6. **Execute with explicit resources.** Use the installed `project-workspace` skill for concrete MCP calls, Kubernetes workspaces/jobs, artifact transfer, hardware leases and recovery. Confirm the worker, namespace, approved image and resource claims before queueing work. Treat external machines, remote clusters and USB/FPGA boards as excluded from the free pool until explicitly authorized and independently confirmed.
7. **Publish and verify.** Resolve the expected Git head, commit to the selected authority, verify the new head and files, and use the provider's actual review mechanism when available. Inspect test/build output and artifacts; do not equate a queued operation, passing mock or open PR with accepted production behavior. Verify cleanup and release of hardware.
8. **Write knowledge only when enabled.** Follow `references/knowledge-standard.md` and the installed `project-knowledge` skill. Distinguish observations, hypotheses, verified findings, decisions, and operational handoffs. Use exact source revisions and artifact digests. Do not quietly save anything to local SQLite or create a shadow knowledge base.
9. **Close the loop.** Reconcile the issue/PR/board status only with observed evidence. Report the exact commit, work item, tests run, resources used, released leases, durable output hashes and outstanding blockers. A handoff is concise state for the next operator, not a transcript.

## Provider and GitHub boundary

- **GitHub source:** use authorized GitHub repository tools for content and commits; use GitHub Issues, pull requests and Projects V2 only if the account and repository actually expose them.
- **Local bare Git source:** use `local:OWNER/REPOSITORY` only if the controller's local Git storage is configured. Exact-commit reads and guarded publication do **not** imply Issues, PRs or Projects. Treat those as unavailable unless a separate provider is selected.
- **Hybrid:** source and knowledge may be local Git while an independent GitHub repository is linked with role `planning`. Track Issues, PRs and a board against that **actual GitHub repository**; do not claim they modify the local source repository. Link local commits and evidence by immutable identifiers or verified URLs.
- **GitLab, Gitea or generic remote Git:** inspect live adapter capabilities; unsupported transports or provider APIs are not assumed operational. Surface the missing capability and retain the working Git operations without fabricated substitutes.
- On every provider, obey branch protections, required reviews, credential scopes and explicit confirmations. Do not self-approve, bypass review requirements, force-push, rewrite unrelated histories or modify account-wide repositories without authority.

Read `references/github-workflow.md` for Issues, Projects V2, branches, PRs, review and provider fallbacks.

## Knowledge and documentation

Use the canonical, human-readable layout in `references/knowledge-standard.md`. Prefer one source of truth for each claim: source code in Git; project status in the canonical work tracker; decisions and evidence in versioned knowledge; large binary evidence in the designated artifact store with a digest-linked manifest. Existing repositories may use equivalent conventions; adapt deliberately instead of making competing hierarchies.

Read `references/knowledge-standard.md` before creating or changing a knowledge repository. Use `scripts/check_knowledge.py` on a local checkout when the standard layout is in use; it is a structural precheck, not a proof of evidence or scientific validity.

## Cluster and delivery discipline

Read `references/cluster-and-delivery.md` when allocating compute, moving files, building OCI images, deploying software, running FPGA/EDA work, recovering devices or declaring acceptance. Compute is an ephemeral execution plane. Neither a Pod filesystem nor an unpushed clone is authoritative source or knowledge. Track image identities, resource requests, exact source commits and artifact digests for reproducibility; avoid copying credentials into jobs or logs.

## Composition and limits

- `professional-code` owns implementation quality, references, tests, technical writing and removal of development residue.
- `project-workspace` owns live compute, storage transport, Kubernetes resource allocation, external-resource operations and exact tool syntax.
- `project-knowledge` owns opt-in persistence, knowledge publication and recovery. This skill specifies the organization and decision procedure.
- This skill does not grant credentials, install connectors, replace a Git provider, create a cluster, establish hardware mappings, guarantee backup recovery or confer permission to merge.

When a referenced skill or live adapter is absent, use available authorized primitives for the supported portion and report the missing capability. Do not silently weaken invariants.
