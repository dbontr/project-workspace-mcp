# Execution and Git knowledge contract

Project Workspace supports both independent tasks and persistent projects. A task may request an authorized worker or named external resource without creating a project. Task execution does not automatically write project knowledge. A project opts into Git-backed knowledge explicitly; otherwise, no project knowledge is retained.

## Execution

A standalone task may request CPU, GPU, NPU, FPGA or USB resources under existing admission and lease policies. Its transient outputs are delivered or explicitly exported. Task coordination records do not constitute a project knowledge store. Promotion to a project requires explicit project selection and authorization.

External machines, EDA guests and other clusters belong in a separate registry rather than the free worker pool. Use requires an exact resource ID, an authorized connection adapter and an explicit lease. Registration alone never implies remote execution capability.

## GitHub

Project workflows default to linked repositories. Owner-authorized account-wide operations may inspect other accessible repositories. Rename and transfer operations require an exact repository identity, explicit confirmation, provider permission checks, audit and verification. GitHub access remains bounded by the connected credential.

## Attachments and study packs

A file-capable MCP client must transmit the attachment bytes. Accept a verified bounded input directly or stream larger files through authenticated upload. A study pack contains source identities, verified locations, selected text, questions and provenance. Do not invent page references. Files from standalone tasks are not automatically written to knowledge.

## Git knowledge layout

The optional `git` policy uses a selected repository and branch, with source code and knowledge either sharing that repository or using separate ones. The `none` policy performs no knowledge writes. A standard repository layout is:

```
knowledge/Project.md
knowledge/Context.md
knowledge/decisions/YYYY-MM-DD-topic.md
knowledge/findings/YYYY-MM-DD-topic.md
knowledge/handoffs/YYYY-MM-DD-topic.md
knowledge/sources/manifest.json
knowledge/study-packs/PACK-ID/manifest.json
```

Knowledge reads resolve the chosen branch to an exact commit SHA. Knowledge commits require expected revision checks and remote read-back. Existing local notes are not evidence of Git persistence and must not be discarded before verified migration.

## Acceptance

Verify project-free task execution without knowledge writes; explicit external resource selection; authenticated and confirmed account-level GitHub operations; attachment integrity; source-cited study packs; and Git knowledge policy enforcement. Preserve existing project, workspace and artifact data.
