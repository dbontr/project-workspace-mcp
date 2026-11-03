# References

- [Model Context Protocol specification, 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18) — MCP maintainers. Used for: Streamable HTTP transport, JSON-RPC initialization, and tool calls.
- [SQLite JSON functions](https://www.sqlite.org/json1.html) — SQLite. Used for: topic enumeration and passage filtering over validated JSON metadata within authorized research libraries.
- [SQLite FTS5 extension](https://www.sqlite.org/fts5.html) — SQLite. Used for: Unicode-aware indexed passage search with project-scoped source joins and bounded results.
- [SQLite C interface](https://www.sqlite.org/cintro.html) — SQLite. Used for: durable transactional project state, prepared statements, and WAL.
- [Go language specification](https://go.dev/ref/spec) — Go team. Used for: Go controller and worker implementation.
- [Go net/http documentation](https://pkg.go.dev/net/http) — Go team. Used for: authenticated controller HTTP service and worker transport.
- [GitHub REST API, Git objects](https://docs.github.com/en/rest/git) — GitHub. Used for: revision-guarded commits, blobs, branches, and draft PRs.
- [GitHub REST API, repository contents](https://docs.github.com/en/rest/repos/contents) — GitHub. Used for: file retrieval and immutable checkout archives.
- [Docker container run](https://docs.docker.com/reference/cli/docker/container/run/) — Docker. Used for: isolated workspaces and quotas.
- [Docker build context](https://docs.docker.com/build/concepts/context/) — Docker. Used for: administrator-controlled build inputs.
- [Docker image save](https://docs.docker.com/reference/cli/docker/image/save/) — Docker. Used for: portable environment archives.
- [NVIDIA Container Toolkit CDI](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/cdi-support.html) — NVIDIA. Used for: approved GPU device identifiers.
- [AMD Vivado supported platforms](https://docs.amd.com/r/2025.2-English/ug973-vivado-release-notes-install-license/Supported-Operating-Systems) — AMD. Used for: vendor toolchain host and license constraints.
- [GitHub Projects V2 GraphQL API](https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-api-to-manage-projects) — GitHub. Used for: native Project association, item pagination, draft issues, issue and pull request linking, and field updates.
- [GitHub GraphQL Projects schema](https://docs.github.com/en/graphql/reference/projects) — GitHub. Used for: ProjectV2 node fields and paginated item/field-value queries.
- [GitHub Projects repository linking](https://docs.github.com/en/issues/planning-and-tracking-with-projects/managing-your-project/adding-your-project-to-a-repository) — GitHub. Used for: distinguishing repositories listed under a Project from repositories with items tracked by a Project.
- [GitHub repository size limits](https://docs.github.com/en/repositories/creating-and-managing-repositories/repository-limits) — GitHub. Used for: keeping compiled artifacts and large datasets outside Git history.
- [Generating a GitHub App installation access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app) — GitHub. Used for: short-lived, restricted installation access tokens.
- [Generating a JWT for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app) — GitHub. Used for: application JWT signing and installation authentication.
- [GitHub REST API: Pull requests](https://docs.github.com/en/rest/pulls/pulls) — GitHub. Used for: revision-guarded PR merging, provider-confirmed merge results, and draft PR operations.
- [GitHub REST API: Checks](https://docs.github.com/en/rest/checks/runs) — GitHub. Used for: check-run status and merge verification.
- [GitHub REST API: Commit statuses](https://docs.github.com/en/rest/commits/statuses) — GitHub. Used for: combined commit status checks before merging.
- [GitHub branch deletion after PR merge](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-branches-in-your-repository/deleting-and-restoring-branches-in-a-pull-request) — GitHub. Used for: branch retention rules and open pull request reference protection.
- [GitHub automatic deletion of merged branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-the-automatic-deletion-of-branches) — GitHub. Used for: opt-in native branch cleanup through repository settings.
- [GitHub pull request reviews](https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/approving-a-pull-request-with-required-reviews) — GitHub. Used for: distinction between an author's self-verification and independently approving a PR.
- [Restic backup and relative paths](https://restic.readthedocs.io/en/stable/040_backup.html) — Restic maintainers. Used for: encrypted snapshot export and relative directory layout.
- [Restic restoring snapshots](https://restic.readthedocs.io/en/stable/050_restore.html) — Restic maintainers. Used for: explicit snapshot and subdirectory restoration into clean destinations.
- [Restic repository checks](https://restic.readthedocs.io/en/stable/045_working_with_repos.html) — Restic maintainers. Used for: repository integrity verification and full-data recovery audits.
- [Restic scripting and environment variables](https://restic.readthedocs.io/en/stable/075_scripting.html) — Restic maintainers. Used for: environment-backed repository selection and protected password-file configuration.
- [SQLite transactions](https://www.sqlite.org/lang_transaction.html) — SQLite. Used for: serialized, fenced task claim acquisition and completion.
- [SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html#vacuuminto) — SQLite. Used for: consistent SQLite database snapshots during offline backup.

- [LTspice official simulator downloads](https://www.analog.com/en/resources/design-tools-and-calculators/ltspice-simulator.html) — Analog Devices. Used for: official Windows and macOS platform support and publisher download origin in native software catalog guidance.
- [Debian apt-get manual](https://manpages.debian.org/stable/apt/apt-get.8.en.html) — Debian. Used for: operator-approved apt package installation in versioned OCI environments.

- [RFC 7519: JSON Web Token](https://www.rfc-editor.org/rfc/rfc7519) — IETF. Used for: JWT claim validation and bearer-token expiration.
- [RFC 7517: JSON Web Key](https://www.rfc-editor.org/rfc/rfc7517) — IETF. Used for: JSON Web Key Set parsing and key selection.
- [RFC 7518: JSON Web Algorithms](https://www.rfc-editor.org/rfc/rfc7518) — IETF. Used for: RS256 signature validation.
- [RFC 9728: OAuth 2.0 Protected Resource Metadata](https://www.rfc-editor.org/rfc/rfc9728) — IETF. Used for: OAuth resource metadata and bearer challenges.

- [systemd service execution settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html) — systemd maintainers. Used for: service credentials, restricted state directories, environment files, and filesystem hardening.
- [Docker Engine Linux post-installation](https://docs.docker.com/engine/install/linux-postinstall/) — Docker. Used for: systemd Docker startup and the root-equivalent trust boundary of the docker group.
- [Docker container listing filters](https://docs.docker.com/reference/cli/docker/container/ls/) — Docker. Used for: worker-scoped managed-job inventory and crash reconciliation.
