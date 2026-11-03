---
name: project-workspace
description: Use a self-hosted Project Workspace MCP server to continue projects with persistent notes, linked GitHub repositories, isolated distributed workers, interactive file and terminal operations, and durable artifacts. Apply for project coding, research, tests, and computation using the user's connected project workspace.
---

# Project Workspace

Operate the project as the unit of work. Use the MCP tools exposed by the selected user's controller; do not assume a named controller is connected merely because this skill is installed.

1. **Resolve context.** List projects, retrieve versioned notes and search relevant project knowledge. List linked GitHub repositories. Do not infer that a repository link grants GitHub write access.
2. **Check resources.** List compute nodes and active project workspaces. Distinguish advertised capacity from confirmed reservations. Create a bounded workspace for the workload and wait until it reports `ready`.
3. **Attach sources.** Use `github_checkout_repository` with the desired repository and revision. Wait for the import operation to succeed. Note the exact returned commit SHA. Repositories are staged beneath `/workspace/repos/OWNER__REPO`.
4. **Execute.** Call `workspace_call` with `command`, `read_file`, `write_file`, `list_directory`, or `search`. Every call returns a queued operation ID. Poll `workspace_operation` until it has a terminal state. For commands, inspect `exit_code` and stderr; operation success alone does not mean a command's tests passed. Use `expected_sha256` when updating known files.
5. **Preserve work.** Export final files with `export_file`; verify the operation result and artifact metadata before relying on it. For GitHub publication, pass selected artifact IDs and an exact expected base commit to `github_publish_artifacts`. Confirm the draft PR response; never claim a branch was merged.
6. **Record decisions.** Read the current note revision and use `write_project_note` with `expected_version`. On a conflict, re-read and reconcile. Distinguish hypotheses, proposed changes, and verified experimental outcomes.
7. **Release.** Queue `workspace_destroy`, inspect its terminal operation and the workspace state, then report results. Unexported files may remain only in a worker's Docker volume; they are not a durable controller artifact.

Never send GitHub credentials, host SSH keys, or administrator tokens into a worker operation or project note. Never execute on the worker host or bypass its container boundary. Do not treat queues, requests, or speculative plans as evidence of completed work.
