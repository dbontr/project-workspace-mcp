# Project Workspace MCP

A self-hosted, project-aware distributed computing system for MCP clients. Its Go controller retains projects, repository associations, knowledge, durable artifacts, environment definitions, hardware leases, jobs, and execution history. Linux Go workers run isolated Docker workspaces on allocated compute nodes. The controller and worker require **no Python interpreter or Python application server**. Python, Vivado, C++, and other applications can be installed within approved workspace images.

The controller uses SQLite and the system SQLite library through Go's CGO interface. Existing Project Workspace SQLite databases retain their schema and records. Do not run two authoritative controllers against the same database simultaneously. Back up `state.db` together with the sibling `artifacts/` directory before migrating a deployed instance.

## Requirements

- Go 1.23 or later at build time, and a C compiler plus SQLite development library for the controller (`pkg-config sqlite3`). Prebuilt binaries do not require a Go compiler or Python installation.
- Linux and Docker on each execution worker. The controller can run on supported hosts that provide SQLite and a C compiler at build time.
- A trusted MCP client. The HTTP MCP endpoint implements stateless Streamable HTTP with the 2025-06-18 protocol, including `initialize`, `tools/list`, and `tools/call`.
- An HTTPS ingress for remote connections; the controller listens on loopback by default. The owner credential is always available for administration. Optional OIDC bearer-token verification and per-project grants require an external identity provider.

## Install and start

```sh
./scripts/install.sh
~/.local/bin/project-workspace-controller init
~/.local/bin/project-workspace-controller controller
```

To build without the script:

```sh
CGO_ENABLED=1 go build -o project-workspace-controller ./cmd/project-workspace-controller
go build -o project-workspace-worker ./cmd/project-workspace-worker
./project-workspace-controller init
./project-workspace-controller controller
```

`init` generates an owner-only credential at `~/.config/project-workspace/admin.token`. The server listens at `http://127.0.0.1:8000` and stores its data in `~/.local/share/project-workspace/state.db` unless `PWM_DB` overrides the path. MCP is at `/mcp/`; the local operator dashboard is `/dashboard`. Use `PWM_ADMIN_TOKEN` to supply a credential from a secrets manager without a token file. Never share this credential with an untrusted client. For a reverse proxy, set `PWM_ALLOWED_HOSTS` and `PWM_ALLOWED_ORIGINS` to the intended public host and browser origins. `PWM_ALLOW_REMOTE_LISTEN=1` permits binding away from loopback only when the operator provides a protected TLS ingress.

The server's configuration variables include `PWM_GITHUB_TOKEN`, `PWM_GITHUB_GENERIC_WRITES=1` (opt-in repository mutations), and `PWM_MAX_ARTIFACT_BYTES` (default 2 GiB).


### Managed Linux services

On a conventional Linux host with systemd, a writable `/etc` and `/usr/local`,
and the build dependencies listed above, install a controller service:

~~~sh
sudo sh scripts/install-systemd.sh controller
sudo systemctl status project-workspace-controller
curl --fail http://127.0.0.1:8000/health
~~~

The installer builds the Go executable unless `--binary PATH` identifies an
existing executable. It creates the `project-workspace` service account, stores
the SQLite database at `/var/lib/project-workspace/state.db` and the
administrator token at
`/var/lib/project-workspace/.config/project-workspace/admin.token`, and
enables a loopback-only controller service. It does not overwrite existing
data or issue a new administrator token when one exists. Configure
`/etc/project-workspace/controller.env` (root-only mode `0600`) for
approved OIDC, GitHub App, backup and public-origin settings. A remote
worker or MCP client must connect through an operator-managed HTTPS reverse
proxy with correct host/origin allowlists and identity policy. A loopback
listener alone does not provide remote access.

On the controller, obtain a single-use pairing code:

~~~sh
sudo runuser -u project-workspace -- env HOME=/var/lib/project-workspace \
  PWM_DB=/var/lib/project-workspace/state.db \
  /usr/local/bin/project-workspace-controller pair
~~~

On a **separate** Linux worker with a running rootful Docker daemon, create a
private file containing only that code and enroll the worker:

~~~sh
umask 077
touch "$HOME/worker-pairing-code"
$EDITOR "$HOME/worker-pairing-code"
sudo sh scripts/install-systemd.sh worker \
  --controller https://controller.example.com --node compute-one \
  --pairing-code-file "$HOME/worker-pairing-code"
rm -f "$HOME/worker-pairing-code"
sudo systemctl status project-workspace-worker
~~~

The worker installer refuses a non-HTTPS address, unsafe pairing-code file
permissions and conflicting existing node configuration. It creates a
separate `project-workspace-worker` account with membership in the
`docker` group, enrolls with `--code-stdin` so the one-time code is not
placed in process arguments, and stores the distinct worker credential
under `/var/lib/project-workspace-worker/.config/project-workspace/workers/`.
The worker service reads the controller address and node ID from the
root-owned `/etc/project-workspace/worker.env`. On subsequent installs
with unchanged settings, the existing credential is retained and the code
file can be omitted. Operator-approved image and device settings may be
added to that environment file. The Docker group confers root-equivalent
host privileges: enroll only trusted machines and operators.

Services are restarted on failure but persist state across restarts. To
inspect them, use `journalctl -u project-workspace-controller` or
`journalctl -u project-workspace-worker`. To stop without deleting project
data, use `systemctl disable --now` with the corresponding service name.
Before replacing an established deployment or attempting recovery, retain
the controller's SQLite database and sibling artifacts together; prefer
verified off-host encrypted backups. Systemd packaging does not establish
successful two-host pairing, Docker execution, TLS configuration or disaster
recovery without separate live acceptance tests. Declarative systems such as
NixOS require equivalent native service definitions rather than this
mutable-host installer.

## OAuth/OIDC identities and project permissions

The controller can act as an OAuth 2.0 protected resource, validating signed
RS256 JWT bearer tokens issued by a configured OIDC provider. The provider
performs login, authorization, consent, and token issuance. This controller
does not issue OAuth tokens or provide its own login page. Stateless JWTs may
remain cryptographically valid until expiry even after provider-side revocation.

Configure all three variables on the controller:

```sh
PWM_OIDC_ISSUER=https://identity.example.com
PWM_OIDC_AUDIENCE=project-workspace
PWM_OIDC_JWKS_URL=https://identity.example.com/.well-known/jwks.json
PWM_PUBLIC_URL=https://workspace.example.com
```

Use the actual issuer, resource audience, and JWKS endpoint provided by your
identity service; the example values are illustrative, not universal defaults.
The public URL enables OAuth Protected Resource Metadata at
`/.well-known/oauth-protected-resource`. Without all OIDC variables,
authentication remains owner-token-only. Configure an HTTPS ingress and an
OAuth client separately; providing the server configuration does not
automatically connect ChatGPT or authorize any new users.

On successful authentication, `GET /api/whoami` returns the stable principal
`oidc:ISSUER#SUBJECT`. The administrator assigns a role in a specific
project with a revision-guarded request:

```sh
curl -fsS -X POST "$BASE/api/projects/research/permissions" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"principal":"oidc:https://identity.example.com#stable-user-subject","role":"viewer","expected_role":""}'
```

Roles are `viewer`, `editor`, and `owner`. A viewer can inspect permitted
project records, notes, and artifacts. An editor can submit isolated compute
work and manage files and environments. A project owner can administer that
project's access grants; destructive USB allocations and privileged GitHub
mutations remain restricted by additional checks. A grant does not imply
access to any other project. Non-administrators see only their assigned
projects in `GET /api/projects` and `list_projects`.

Update a grant by supplying its current role as `expected_role`. Revoke it
using `"role":"revoke"` and the exact prior role. This immediately removes
project access even if the external identity token has not expired. To reject
one token irrespective of project grants, its holder may read the
`token_sha256` fingerprint from `GET /api/whoami`. An administrator can
POST `{"sha256":"FINGERPRINT","expires":UNIX_EXPIRATION}` to
`/api/revocations`; the controller never needs the bearer token itself.
The expiration must be within the next 24 hours. The
controller checks token issuer, audience, signature, expiration, and
not-before time; JWKS keys refresh every five minutes. Unavailable key
services fail closed. Access decisions are recorded in the SQLite ledger,
which the administrator can inspect through `GET /api/access-audit`.

The controller's owner token can administer all projects and should remain
only in a trusted secret store. The worker's separate pairing credentials
and project hardware leases are not OAuth bearer tokens. A production
deployment requires a real issuer, live token validation and rejection tests,
an authorization-code client flow, and provider revocation acceptance before
untrusted access is enabled.

## GitHub App installation credentials

The Go controller can obtain short-lived GitHub installation credentials
instead of retaining a long-lived personal access token. This is optional
and requires an operator-owned GitHub App installation:

```sh
PWM_GITHUB_APP_ID=123456
PWM_GITHUB_INSTALLATION_ID=12345678
PWM_GITHUB_APP_KEY_FILE=/etc/project-workspace/github-app.pem
```

The PEM file must be a regular, owner-only file (`0600`), containing a
2048-bit-or-stronger RSA private key. The controller signs a nine-minute
application JWT and exchanges it for short-lived installation access tokens.
It caches credentials in memory until two minutes before expiry. Repository
REST operations request tokens restricted to the corresponding repository
name; GitHub Projects V2 GraphQL operations require access to the
installation's selected repositories and Projects resources. Each App
installation belongs to a GitHub owner or organization: a second owner may
require a separate installation, which this single-installation configuration
does not yet manage.

When these three App variables are supplied, the controller uses GitHub App
credentials rather than `PWM_GITHUB_TOKEN`. If any App setting is invalid,
startup fails rather than silently falling back to a broader token.
GitHub permissions still govern issue, pull request, contents, administration,
and Projects V2 operations. The App must be installed on the intended
repositories with the exact required permissions. Enabling branch
auto-deletion requires repository administration permission; changing the
token alone does not expose a missing connector operation. Installation keys
and tokens never enter worker payloads or repository archives.

A live GitHub App connection and permissions acceptance test is required
before treating this option as configured in production.

## Pair a compute node

On the controller:

```sh
project-workspace-controller pair
```

On a trusted Linux worker:

```sh
project-workspace-worker doctor
project-workspace-worker join --controller https://controller.example --node worker-one --code PAIRING_CODE
project-workspace-worker serve --controller https://controller.example --node worker-one
```

A pairing code is single-use and expires after ten minutes. Each worker has a distinct, revocable credential. The worker contacts the controller over outbound HTTPS and claims work with expiring fenced tokens. Intermittent network failures use bounded retries; failed cleanup never releases a leased device prematurely.

## Projects, workspaces, and environments

A project owns knowledge, linked GitHub repositories, artifacts, and execution history. An environment describes a versioned OCI image containing applications and dependencies. A workspace contains mutable project files. Hardware allocations are separate exclusive leases. These objects are not interchangeable.

The default workspace image is `debian:bookworm-slim`, with the worker's `PWM_ALLOWED_IMAGES` controlling approved images. Use a Python image or an administrator-built Vivado image explicitly when needed. Define a named environment with a Dockerfile or a previously approved base image, run `environment_build`, and wait for the operation to succeed. The `environment_export` and `environment_import` tools transfer saved images; `workspace_snapshot` and `workspace_restore` save and restore files independently. Neither operation snapshots running process memory.

To create a project and workspace over REST:

```sh
TOKEN=$(cat ~/.config/project-workspace/admin.token)
BASE=http://127.0.0.1:8000
curl -fsS -X POST "$BASE/api/projects" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"id":"research","title":"Research"}'
curl -fsS -X POST "$BASE/api/workspaces" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"project_id":"research","cpu":2,"memory_mb":4096}'
```

Workspace creation queues a `start` operation; it is not synchronous. Poll `GET /api/projects/research/workspaces` until the workspace is `ready`. Use `workspace_call` to run commands and edit files; poll `workspace_operation` for worker-confirmed results. `exit_code != 0` means a command failed even when the operation itself completed. Export important artifacts before stopping the workspace. Named volumes are retained for deliberate recovery and cleanup.

## Resource feasibility and worker selection

`resource_plan` and `POST /api/projects/PROJECT/resource-plan` inspect current
controller heartbeats, reserved CPU/memory, exclusive device leases, approved
OCI images, environment placement and worker architecture. They return a
ranked list of eligible and rejected nodes with individual explanations.
A plan does **not** reserve capacity. The local Go/Docker worker scheduler
rechecks all admission conditions when creating the workspace.

```sh
curl -fsS -X POST "$BASE/api/projects/research/resource-plan" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cpu":4,"memory_mb":8192,"hardware":{"gpu":1},"min_gpu_memory_mb":8192}'
```

The planner prefers adequate non-accelerated nodes for CPU-only work and
avoids occupying scarce GPUs or NPUs unnecessarily. A GPU memory requirement
compares the operator-reported total VRAM per GPU; it is **not** a measurement
of free VRAM inside a shared GPU. Whole-device lease exclusivity remains
mandatory. Devices with missing VRAM information cannot satisfy a nonzero
minimum. `architecture` may be `amd64` or `arm64`, and `node_id` may
require an exact approved worker. After inspecting the plan, an authorized
client may pass `node_id` and `min_gpu_memory_mb` to `workspace_create`.
The controller then enforces the requested node and device constraints
during actual allocation. The dashboard offers a resource preview before
workspace creation.

This planner covers the Go controller's registered Docker workers. It does
not schedule Kubernetes, Slurm, a university cluster, Windows desktop
applications, or macOS hosts, and it cannot establish software licensing
or actual application execution solely from image metadata. External
adapters and independent scheduling permissions require separate acceptance.

## GPU, NPU, FPGA, and USB access

A node's operator supplies an allowlisted device manifest. The controller never accepts a host device path from an AI tool. Explicit leases reserve whole device identifiers for one workspace at a time. GPU access uses an administrator-configured CDI identifier; vendor FPGA/NPU hardware can use approved character devices and vendor runtime images.

USB access is **off by default**. The node operator enables it using `project-workspace-worker serve --enable-usb`, and the workspace must separately request `usb_passthrough=true`, `usb_node=<exact worker>`, and `usb_device_id=<exact device>`. The worker validates stable USB disk aliases, detects mounted or mapped storage, and rejects unsafe device paths. A hardware lease remains active until confirmed cleanup. The AI must not choose an arbitrary disk or infer authorization to erase one. Flashing an OS image is destructive: the intended USB device must be independently verified before issuing any write command.

Large OS images can be streamed to `PUT /api/projects/PROJECT/artifacts/upload` with `X-Artifact-Name`, `X-Content-SHA256`, and the owner bearer credential. Use `POST /api/projects/PROJECT/workspaces/WORKSPACE/artifacts/import` to stage the verified artifact in a workspace. The artifact path is relative to the workspace, and imports refuse unintended overwrites.

## Personal tasks and independent files

The controller now supports owner-scoped **standalone tasks** without requiring
a registered research project. Task metadata (title, description, tags and
active/archived status) uses revision guards. Each task file retains every
immutable revision rather than overwriting previous contents. The authenticated
OIDC subject or trusted controller owner identity is attached server-side, and
another subject cannot enumerate, read, modify, or download its files.

The MCP tools are `task_create`, `task_list`, `task_details`,
`task_update`, `task_files`, `task_file_info`, `task_save_text`,
`task_send_base64`, `task_read_base64` and `task_export_project`.
Small binary inputs up to 512 KiB use base64 plus a mandatory SHA-256. Larger
inputs use authenticated streamed REST uploads with bounded size and exact
checksum verification:

```sh
curl -fsS -X POST "$BASE/api/tasks" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Independent simulation","description":"Circuit experiments","tags":["circuits"]}'

# After using the task ID returned above:
SHA=$(sha256sum simulation.raw | cut -d ' ' -f1)
curl -fsS -X PUT "$BASE/api/tasks/TASK_ID/files/simulation.raw" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Expected-Revision: 0" \
  -H "X-Content-SHA256: $SHA" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @simulation.raw
```

The initial file revision is 1; upload another version by setting
`X-Expected-Revision` to the latest returned revision. A stale revision,
changed checksum, oversize payload, archived task, or different owner is
rejected. Download the latest with
`GET /api/tasks/TASK_ID/files/simulation.raw/download`, or provide
`?revision=N` to retrieve a historical version. Task records are retained
after archiving; no host files are removed.

Files use the existing immutable artifact storage and therefore participate in
the verified database-plus-artifact backup manifest. An explicitly approved
promotion through `task_export_project` or
`POST /api/tasks/TASK_ID/files/simulation.raw/promote` creates a durable
project artifact; project editor permission is checked separately. The source
task remains intact. Afterwards, the usual `workspace_import_artifact`
can stage the promoted file onto a worker.

### Cooperative claims on standalone tasks

Independent personal tasks also support expiring, owner-scoped work claims.
Before a duplicate-prone operation, derive a stable `work_key` from the
objective, exact input checksum and method. Call `personal_task_claim_list`
before `personal_task_claim`, or POST to
`/api/tasks/TASK_ID/claims` with `{"work_key":"analyze-input-sha256",
"lease_seconds":900}`. The controller returns `claimed`, `busy`, or
`completed`. Only a claimant receiving `claimed` and its one-time
`lease_token` may begin work.

The lease token is never included in listings, logs or persisted task files.
Renew through `personal_task_claim_renew`, and complete through
`personal_task_claim_finish` with immutable file artifact IDs from that
same personal task. A missing or unrelated file ID is rejected. A failed
or canceled attempt is `released` without marking completion.
Expiration increments the generation on the next claim; a prior claimant
cannot renew or complete a replacement lease.

These claims are **cooperative duplicate prevention**, not physical
device reservations or automatic worker allocations. They do not replace
project task claims, source conflict control or the separate worker
scheduler. Archived tasks retain their completed and historical claims,
but do not admit new work.

This is the **storage and transfer API**, not automatic ChatGPT attachment
resolution. A connected, file-capable ChatGPT client/adapter must still pass
the real attachment bytes or call the REST stream route. The task library
does not yet provide standalone claim leases, automatic task-to-worker
creation, or independent native execution without project resource grants.
Do not claim end-to-end ChatGPT attachment transfer until the actual client
integration has been tested.

## Cooperative task claims across chats

A project may have several agents or worker sessions. Before expensive or
duplicate-prone work, derive a stable `work_key` from the exact source
revision, objective and method. Call `task_claim` (or
`POST /api/projects/PROJECT/claims`) to reserve that unit of work.
A claim has an expiring lease and a one-time, secret `lease_token`, distinct
from the worker's resource lease and GitHub credentials.

The returned `status` is `claimed`, `busy`, or `completed`. Only a
`claimed` response authorizes this client to execute the work. If it is
`busy`, inspect `task_claim_status` instead of running the same job. If
`completed`, reuse the saved result artifact IDs instead of repeating the
task. The default lease is 900 seconds, with a supported interval of
60–3600 seconds. Renew before expiry:

```sh
curl -fsS -X POST "$BASE/api/projects/research/claims" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"work_key":"compile-revision-123","lease_seconds":900}'
```

Keep the returned `lease_token` private. Pass the same
`lease_token`, `generation`, and work key to `task_claim_renew`, then
`task_claim_finish(status="completed", artifact_ids=[...])` after verifying
the output. The controller verifies that every output artifact belongs to
the project. `status="released"` abandons the claim without recording a
completion. On expiry, another client may claim the work, with a strictly
higher generation; an earlier claimant cannot finish or renew the replacement
claim. Claims and completion survive controller restarts.

This coordinates cooperating agents. It does not reserve CPU, memory,
hardware, GitHub branches or remote workers. Use separate workspace
leases, Git revision guards and the scheduler for those resources. An agent
that still holds a process after its task lease expires must stop it before
another claimant resumes equivalent work.

## Approved images and software packages

The controller has an administrator-managed image and software pool. A pool
entry does **not** automatically install an image, authorize a worker, or
grant access to hardware. Workers report the exact OCI references approved by
their local `PWM_ALLOWED_IMAGES` setting; `pool_list_images` displays
the currently eligible online workers. Images in the pool can be used in
`workspace_create(image_pool_id="...")`, which refuses to schedule a worker
that has not explicitly approved the selected image. Ordinary direct image
and versioned environment options remain supported.

```sh
curl -fsS -X POST "$BASE/api/pool/images" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"debian-bookworm","image":"debian:bookworm-slim","description":"Debian base"}'
curl -fsS "$BASE/api/pool/images" -H "Authorization: Bearer $TOKEN"
```

The software catalog contains immutable, curated apt package sets associated
with one Debian or Ubuntu base image. It never accepts shell commands as
package names. Non-apt toolchains and licensed applications should instead
be provided as approved, versioned OCI images. Register packages and compose
an environment before requesting a worker build:

```sh
curl -fsS -X POST "$BASE/api/pool/software" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"c-toolchain","image_id":"debian-bookworm","packages":["gcc","make"]}'
curl -fsS -X POST "$BASE/api/projects/research/environments/from-pool" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"c-build","image_id":"debian-bookworm","software_ids":["c-toolchain"],"expected_version":0}'
```

Then start a base-image workspace, call `environment_build`, await
worker-confirmed success, and start a workspace selecting the ready environment.
The build requires network access (`PWM_BUILD_NETWORK=1` on the authorized
worker) or an accessible local apt cache. Builds do not bypass image or
hardware policy, provide a private registry, redistribute proprietary software,
or guarantee availability of packages in upstream repositories.

### Software catalog administration

A program request is separate from an installable package or OCI image.
Operators can register a program without asserting that an executor exists.
The `/dashboard` software catalog provides forms to register images and apt
packages, request programs, review their source and license, and disable,
reactivate, or retire catalog entries. Equivalent owner-only operations are
available through MCP and REST.

To request a native application such as LTspice:

```sh
curl -fsS -X POST "$BASE/api/pool/programs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "id":"ltspice",
    "name":"LTspice",
    "platform":"windows-native",
    "distribution":"native",
    "version":"26",
    "source_url":"https://www.analog.com/en/resources/design-tools-and-calculators/ltspice-simulator.html",
    "license_notes":"Review vendor download and use conditions before installing"
  }'
```

The initial program status is `requested`, not `installed`. An
administrator can record a license/source review with `pool_review_program`
or `POST /api/pool/programs/ltspice/review` with
`{"status":"reviewed","expected_revision":1,"review_note":"..."}`.
A native program cannot be made available through a Linux Docker environment
unless a separately authorized and verified native adapter exists.
This catalog does not download, redistribute or silently execute vendor
installers.

Linux package requests can progress to `available` after review and after
linking to an active, approved apt package collection. OCI-based programs
similarly require an approved image. A reviewed program is not an installed
application. To create a runnable environment, select its approved image or
software entry, compose a versioned definition, build it on an eligible
worker, and verify the resulting operation.

For an image or apt package collection, an administrator may call
`pool_update_entry`, or POST to
`/api/pool/images/ENTRY_ID/state` or
`/api/pool/software/ENTRY_ID/state`:

```sh
curl -fsS -X POST "$BASE/api/pool/software/c-toolchain/state" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"disabled","expected_revision":1,"reason":"Replacing toolchain"}'
```

Status transitions use optimistic revision checks and a durable audit history,
available at `GET /api/pool/events?kind=software&id=c-toolchain` or the
`pool_events` tool. `disabled` entries can be reactivated after review;
`retired` entries are permanent catalog tombstones. They are omitted from
new catalog-based allocations and environment compositions, but existing
containers, exported image archives, environment definitions and output
artifacts remain untouched. To update an immutable package list, register a
new collection and retire the superseded entry. To remove the binary from a
host, use a separately approved worker/image cleanup or native installation
adapter after ensuring no retained workspace depends on it. A catalog
operation is **not** a host uninstall.

### File transfer over MCP

The `artifact_send_base64` tool accepts `project_id`, `name`,
`sha256`, and `data_base64` for files up to **512 KiB**. The controller
verifies the digest before persisting them. `artifact_info` reads metadata
and `artifact_read_base64` retrieves a verified small file. Use
`workspace_import_artifact` to stage it in a ready worker, or export a worker
file as a durable artifact. These operations are project-scoped and require
the existing owner credential.

For larger PDFs, images, notebooks, datasets, and OS images, use the
authenticated streamed REST upload `PUT /api/projects/PROJECT/artifacts/upload`
and download `GET /api/projects/PROJECT/artifacts/ARTIFACT_ID`.
The content hash is mandatory. The current MCP attachment tool takes base64
text; an integration that converts ChatGPT's native file attachments to this
transport requires a separate client-side file-capable connector. It is not
automatically established by installing this repository.

## Source-linked research ingestion and search

Shared research libraries retain the **original** binary paper, dataset or
document as an immutable content-addressed library object, or a Git source
pinned to an exact 40-character commit SHA. Derived passages belong to that
source and never replace or modify the original evidence. A controller
administrator registers the source with `research_register_source`, then
ingests bounded passages with `research_ingest_passages`.

A passage retains a stable source ID, page or section, optional line range,
ordinal, topics, checksum, and the source's object digest or pinned Git
revision. Index batches accept up to 64 passages and 8 KiB of UTF-8 text
per passage. Every batch requires `expected_revision`; simultaneous
writers must re-read before retrying. `append` adds a batch to the current
index epoch, while `replace` creates a new epoch without deleting the old
passages or original document.

For example, after storing an original under the shared research library
`science` as the immutable object `lecture.pdf`:

```sh
curl -fsS -X POST "$BASE/api/libraries/science/research/sources" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Vector analysis lecture","origin_kind":"object",
       "origin_name":"lecture.pdf"}'
# Use the registered source ID returned above:
curl -fsS -X POST "$BASE/api/libraries/science/research/sources/SOURCE_ID/passages" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"mode":"append","expected_revision":0,"passages":[
        {"ordinal":1,"page":2,"section":"Cylindrical basis",
         "text":"The unit basis changes direction with azimuth.",
         "topics":["vector-analysis"]}
      ]}'
```

A project must explicitly link the shared library before its viewer/editor
principals can search it. Read through `research_sources`,
`research_search`, and `research_read_passage`, or the corresponding
`GET /api/projects/PROJECT/research/sources`,
`GET /api/projects/PROJECT/research/search?q=azimuth`, and
`GET /api/projects/PROJECT/research/passages/PASSAGE_ID` routes.
Search uses SQLite FTS5, returns at most 100 bounded excerpts, and
restricts results to the requesting project's linked libraries.
The full passage read verifies its SHA-256. Historical citations remain
readable after reindexing while searches use only the current epoch.

Topic discovery is available through `research_topics` and
`GET /api/projects/PROJECT/research/topics`. Each entry reports the topic
label, number of indexed passages, and distinct sources in the project's
linked libraries. Browse a label through `research_browse_topic` or
`GET /api/projects/PROJECT/research/topics/TOPIC`, which returns
source-linked passages with their stable citation IDs. Topic browsing
uses SQLite JSON indexing functions over the visible source epochs and
never exposes sources from unlinked libraries. It is a navigational view,
not an independently authored topic summary or knowledge graph.

This is **source-linked lexical retrieval**. It is not a semantic embedding
index, PDF parser, or automatic source upload from ChatGPT. A trusted
preprocessor or file-capable connector must upload the original bytes and
provide passages with correct source locations. Do not fabricate page
numbers or assume metadata is verified merely because a model supplied it.
FTS5 is required in the controller's linked SQLite library; a build without
FTS5 cannot provide this index. Indexed history consumes database space,
so operators should monitor growth before indexing very large corpora.

## Project context and immutable research contributions

Canonical context uses the existing revision-guarded project notes with names
`project` (equivalent to `Project.md`) and `context` (equivalent to
`Context.md`). `knowledge_context` returns both notes, the latest handoff,
recent decisions and linked repository metadata. The controller identifies
itself as `controller-local`: this does not silently replace a separately
configured authoritative Project Knowledge store.

Research notes, findings, decisions and handoffs can also be saved as
**immutable contributions**, so parallel workers do not overwrite one another.
Each contribution retains a SHA-256 digest, authenticated author, creation
time, and optional source artifact ID or linked repository with an exact
40-character commit SHA. The controller verifies that referenced artifacts
and repositories belong to the project. Moving branch names are not valid
immutable revision provenance.

The MCP tools are `knowledge_context`, `knowledge_contribute`,
`knowledge_contributions`, `knowledge_contribution` and
`knowledge_search_contributions`. The REST equivalents are:

```sh
curl -fsS -H "Authorization: Bearer $TOKEN" "$BASE/api/projects/research/knowledge/context"
curl -fsS -X POST "$BASE/api/projects/research/knowledge/contributions" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kind":"handoff","title":"Verified phase result","body":"Tests passed; next action recorded"}'
```

Viewer grants permit reading context and contributions; editors can append.
The authenticated caller is recorded by the controller, never accepted from a
user-controlled request body. Listing and search return bounded excerpts and
stable IDs; reading a contribution validates its body checksum. Canonical
note updates continue using `expected_version`. These operations do not
automatically extract PDF pages, index uploaded documents, or create an
off-host backup. They preserve context in the controller's database and
therefore require the same verified backup and restore practices.

## GitHub and persistent knowledge

Associate each GitHub repository with its project using `link_github_repository`. GitHub credentials stay exclusively on the controller; workers receive immutable archives rather than repository tokens. MCP tools support repository information, issue and PR lists, GitHub file reads, checkout by immutable revision, and revision-guarded draft PR publication. Extended repository REST actions are disabled for writes until the administrator explicitly enables `PWM_GITHUB_GENERIC_WRITES=1`.

Project notes use `expected_version` to reject conflicting writes. Search, artifacts, and snapshots are project-scoped. The controller retains the established SQLite table layout so existing records can be opened without a separate conversion script.

## Verification

```sh
go test ./...
go test -race ./internal/controller ./internal/worker
go vet ./...
```

GitHub Actions runs Go unit and protocol tests, cross-architecture builds, and an optional Docker controller/worker integration. The latter requires Docker and a `python:3.12-slim` test image **inside the container only**; no Python interpreter runs on the controller or worker host.

## Checkpoint-preserving workspace shutdown

A normal workspace stop does not automatically save mutable files. For work
that must survive worker teardown, use `workspace_checkpoint_stop` or:

```sh
curl -fsS -X POST "$BASE/api/projects/research/workspaces/WORKSPACE_ID/checkpoint-stop" \
  -H "Authorization: Bearer $TOKEN"
```

The controller appends a `snapshot` operation after previously queued work
and prevents new workspace mutations while the checkpoint is pending. Only
after the worker uploads a verified, durable project artifact and completes
the exact leased snapshot operation does the controller enqueue `stop`.
Poll `workspace_checkpoint_stop_status` or
`GET /api/projects/research/workspaces/WORKSPACE_ID/checkpoint-stop`
for `pending`, `stopping`, `stopped`, `failed`, or `expired`.
Successful records retain both the snapshot ID and stop operation ID.
Repeated requests while pending or stopping are idempotent.

If snapshot creation fails or its operation lease expires while the
workspace lease is still valid, the controller does not enqueue teardown.
The workspace remains eligible for recovery and a new checkpoint request.
An ordinary stop cannot bypass a pending checkpoint, although an
expired workspace lease still triggers mandatory resource cleanup.
A worker crash or lease expiry can therefore prevent preservation of
uncollected files. Schedule checkpoints early rather than waiting until
the lease deadline. A request is rejected when the lease has less than
two minutes remaining.

The durable snapshot appears in `workspace_snapshot_list` for the original
workspace. `workspace_restore` can use its snapshot ID on another ready
workspace belonging to the **same project**, including after the original
worker is no longer available. A queued restore is not proof of completion;
inspect the worker's terminal result and the restored data. Snapshot
archives store workspace filesystem contents, not process memory,
running device state, or an off-host encrypted backup. The stored
artifact only survives controller disk loss when a verified independent
backup includes it.

## Offline recovery snapshots

Stop the controller and all processes that write to its data directory before
backing up or restoring it. The snapshot includes a transactionally consistent
SQLite copy, referenced project artifacts, and referenced content-addressed
library objects. It verifies SHA-256 hashes, detects missing files, and refuses
backups while resumable library uploads remain pending. Unreferenced files and
unfinished uploads are not part of the snapshot.

```sh
project-workspace-controller backup /mnt/off-host/project-workspace-snapshot
```

The destination must not exist. Store the completed snapshot on independent
storage, with restricted access and encryption appropriate to the sensitivity
of project data. A backup left on the same disk is not an off-host backup.
Run recovery on a separate machine or clean data directory:

```sh
PWM_DB=/srv/project-workspace/state.db \
  project-workspace-controller restore /mnt/off-host/project-workspace-snapshot
```

Restore verifies every listed file before writing and refuses to overwrite an
existing database or artifact directory. It does not restore active processes,
worker leases, external GitHub repositories, or external hardware. Reconfigure
controller credentials separately and confirm that workers can be paired again.
Run a full restore exercise on another host before treating backups as reliable.
The basic offline snapshot commands are maintenance operations; the encrypted
Restic path below can be scheduled in-process. Neither provides automatic
multi-controller failover.

## Encrypted off-host backup and disaster recovery

The controller can export an integrity-checked offline snapshot to an
operator-controlled Restic repository. Restic supplies encryption,
deduplicated content-addressed backup storage, snapshot IDs and supported
remote backends. This is an optional **controller-host** dependency: workers
and the Go service never require a Python framework or receive backup keys.

Install Restic on the controller and initialize a repository on **independent
storage** (for example, SFTP or object storage) according to its official
documentation. Store the encryption password in a regular 0600 file readable
only by the controller service user. Configure these environment variables:

```sh
PWM_RESTIC_REPOSITORY=sftp:backup-user@backup-host:/srv/project-workspace
PWM_RESTIC_PASSWORD_FILE=/etc/project-workspace/restic-password
```

The repository URI is illustrative. Provision the remote endpoint and its
access restrictions separately. A local Restic repository is encrypted but
does **not** establish off-host resilience.

For a manually initiated backup, stop the authoritative controller and run:

```sh
project-workspace-controller backup-offsite
```

The command returns a Restic snapshot ID after the existing SQLite, artifact
and library manifest has been uploaded and Restic's repository metadata
check has succeeded. Record this ID in independent recovery documentation;
an upload is not proof of independent-site availability.

Alternatively, a running controller can take snapshots safely using its
**own database instance**, without starting a second controller process:

```sh
PWM_RESTIC_BACKUP_INTERVAL=24h
```

Set this in the controller's environment alongside the repository and
password-file configuration. Intervals shorter than one hour are rejected.
The next snapshot runs after the interval; failures are logged without
claiming success. Do not invoke the offline CLI in parallel with the live
controller: separate processes do not share the in-memory artifact lock.
The in-process scheduler does not provide automated retention policies or
remote-site monitoring. Operators must secure the backup repository, manage
its quota, and verify that the host is independently administered.

On an **independent recovery host**, install the controller and Restic,
configure the same repository and password-file access, then select an
explicit snapshot ID:

```sh
PWM_DB=/srv/project-workspace/state.db \
  project-workspace-controller restore-offsite SNAPSHOT_ID
PWM_DB=/srv/project-workspace/state.db \
  project-workspace-controller verify-offsite SNAPSHOT_ID
```

`restore-offsite` refuses to overwrite existing database or artifact data,
verifies every manifest file and keeps past immutable task file revisions.
`verify-offsite` reads all encrypted repository blobs and completes a
disposable restore with SQLite integrity verification. Run the verification
command on a machine with sufficient space for a full recovery. Verify
authentication, repository access, worker pairing, task files and hardware
leases separately after restoring a real deployment. The remote backup does
not capture an executing process, an uncollected worker filesystem or
GitHub's external state. Successful local CI recovery tests are **not** a
substitute for a remote and independent-host acceptance exercise.

## Operating limits

This is a trusted-machine system with optional OIDC bearer authentication and project grants. It does not provide its own OAuth authorization server, VM-grade hostile-tenant isolation, deployed and independently verified off-host backup/restoration, or verified real GPU/NPU/FPGA/USB hardware passthrough. Those features require separate acceptance before untrusted users or destructive hardware work can be enabled. Proprietary applications such as Vivado are not redistributed by this project. An MCP tool call is not an autonomous reasoning agent.

## GitHub Projects planning and shared research libraries

Project Workspace can use a native GitHub Projects V2 board as the authoritative
planning record. Local projects remain usable without GitHub. GitHub planning
items, custom fields, and the Project README are read live from GitHub; the
controller stores only the verified Project association. The linked board may
contain issues and pull requests from repositories with different owners.

Configure `PWM_GITHUB_TOKEN` **on the controller only**. Its GitHub identity
needs `read:project` for reading Projects or `project` for updating them, as
well as the required repository permissions. To link an existing board by its
ordinary GitHub URL:

```sh
curl -fsS -X POST "$BASE/api/projects/research/planning" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://github.com/users/OWNER/projects/1"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/planning/items"
```

The `github_project_items` MCP tool returns a page of at most 50 live issues,
pull requests, and drafts, including status and other custom fields. Use the
returned cursor to request further pages. `github_project_fields` retrieves the
Project's field IDs and single-select option IDs, including `Status`, and
supports pagination. `github_project_add_item` adds a
linked repository's issue or PR. `github_project_add_draft` creates planning
ideas without inventing a repository issue. `github_project_set_status` sets
an explicitly selected single-select field option. GitHub is the source of
truth; no separate planning-item database is maintained. The dashboard also
provides a GitHub Project URL connection form.

A project may link multiple repositories, including repositories owned by
other accessible organizations. Each association has a role (`source`,
`research`, `lean`, `hardware`, or `documentation`) and an optional preferred
ref. Existing `link_github_repository` calls retain the `source` default:

```sh
curl -fsS -X POST "$BASE/api/projects/research/repositories" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"repository":"OWNER/lean-foundations","role":"lean","default_ref":"main"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/repository-memberships"
```

### Shared Lean, research, and data libraries

Libraries are independent of any one project. Each library may reference
several repositories at **immutable commit hashes**, while any number of
projects can reference the same library. Referencing a library does **not**
automatically grant unrestricted GitHub repository write permissions. The
library catalog supports Lean formalizations, literature, software, and mixed
research collections.

```sh
curl -fsS -X POST "$BASE/api/libraries" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"lean-foundations","title":"Lean Foundations","kind":"lean","description":"Shared mathematical formalizations"}'
curl -fsS -X POST "$BASE/api/libraries/lean-foundations/sources" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"repository":"OWNER/lean-theory","ref":"main","path":"Math","kind":"formalization"}'
curl -fsS -X POST "$BASE/api/projects/research/libraries" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"library":"lean-foundations"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/libraries/lean-foundations/resolve"
```

The source registration resolves `main` to a specific commit at registration
time. To use another revision, explicitly register it again. Source checkouts
stream the GitHub archive to the controller instead of buffering it in memory,
and queue an import into an authorized workspace via `library_checkout_source`.
The same mechanism supports ordinary linked-repository checkouts. Repositories
remain subject to GitHub's own size limits; splitting a large Lean source tree
into independent Lake packages is preferable to keeping an enormous Git
history in one repository.

Compiled `.olean` files, caches, datasets, PDFs, and other large binary objects
belong in the controller's **content-addressed library object store**, not in
Git history. Each object is stored under its SHA-256 digest and can be
referenced from multiple libraries without duplicating its bytes. Names are
immutable; changing an object means creating a different name or library
revision. To register a large artifact (the hash shown must be calculated from
its actual bytes):

```sh
export PWM_MAX_ARTIFACT_BYTES=107374182400  # maximum single upload: 100 GiB
sha256sum lean-cache.tar.zst
curl -fS -X PUT "$BASE/api/libraries/lean-foundations/objects/cache-2026.tar.zst" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Content-SHA256: REPLACE_WITH_SHA256" \
  -H 'X-Object-Kind: build-cache' \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @lean-cache.tar.zst
```

For connections with restrictive upload limits or unstable networks, use
resumable uploads in chunks of at most **8 MiB**. The controller stores the
upload cursor in SQLite, so an interrupted client can query its accepted byte
offset and continue without starting over:

```sh
# The SHA-256 digest must describe the entire source file.
SHA=$(sha256sum lean-cache.tar.zst | cut -d ' ' -f1)
SIZE=$(wc -c < lean-cache.tar.zst | tr -d ' ')
curl -fsS -X POST "$BASE/api/libraries/lean-foundations/uploads" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"name\":\"cache-2026.tar.zst\",\"kind\":\"build-cache\",\"sha256\":\"$SHA\",\"size\":$SIZE}"
```

The response supplies `upload_id`, `received_bytes`, and `chunk_size`.
Upload consecutive binary chunks with `PUT /api/libraries/lean-foundations/uploads/UPLOAD_ID`
and the `X-Upload-Offset` header set to the exact byte position. Read
`GET /api/libraries/lean-foundations/uploads/UPLOAD_ID` after a disconnect;
complete with `POST .../uploads/UPLOAD_ID/complete` once all bytes are accepted.
The controller verifies both total size and SHA-256 before the object becomes
visible. Incomplete sessions expire after 24 hours and can be cancelled with
`DELETE .../uploads/UPLOAD_ID`. A compatible client must not skip offsets or
send more than 8 MiB in one request.

`library_import_object` queues a shared object into a project's ready
workspace. The controller creates an authorized hard link within its artifact
store; it does not copy the entire binary for every linked project. The worker
retrieves the bytes through its existing authenticated, digest-checked artifact
transfer and refuses accidental overwrites. The object store is limited by
available persistent disk and the configured per-upload maximum, not by
GitHub's repository guidelines. It still needs an independently tested,
off-host backup and storage-capacity monitoring. **It is not an off-host
backup solution.**

You can keep private source repositories and public planning boards separate,
but do not publish confidential research in Project items. Owner-token access
is still required for all library operations; a production multi-user
permission system is not yet implemented.

## GitHub issues, branches, and pull-request lifecycle

Each project can link multiple GitHub repositories. All GitHub operations act on
an explicitly linked repository; repository credentials remain on the Go
controller. GitHub Projects V2 remains the planning authority. Issues, commits,
reviews, checks, merges, and branch refs remain authoritative on GitHub.

The MCP tools provide these operations without enabling unrestricted GitHub REST
writes:

- `github_get_issue`, `github_create_issue`, `github_update_issue`, and
  `github_comment_issue` manage issues and their discussions. Updates require
  `expected_state` to detect stale issue status.
- `github_list_branches` and `github_create_branch` work with branches. The
  controller creates only `pwm/`-prefixed branches from a verified Git revision.
- `github_get_pull_request`, `github_create_pull_request`, and
  `github_update_pull_request` inspect and edit PRs with explicit head SHA checks.
  Draft PRs can be marked ready with `github_mark_pull_request_ready`.
- `github_verify_pull_request` reports the current PR head revision,
  mergeability, check runs, and combined commit status. A PR with missing,
  pending, or failing check runs is not considered verified for merging.
- `github_merge_pull_request` requires `confirm=true` and `expected_head` set to
  the exact 40-character commit SHA. It verifies checks, requests a GitHub
  merge with that SHA, and confirms the resulting merged PR. GitHub enforces
  branch protection, review requirements, and merge permissions. This allows
  an agent to merge a PR it authored **when GitHub permits it**, but it cannot
  approve its own PR as an independent reviewer.
- A successful merge deletes the agent-managed `pwm/` head branch by default
  if it remains unchanged, belongs to the same repository, is unprotected,
  and is not referenced by any open PR. If cleanup fails, the merge result
  still reports success together with the cleanup failure; do not retry the
  merge. `github_cleanup_merged_branch` accepts an explicit merged PR number,
  exact head SHA, and confirmation for older branches. It does not delete
  unrelated or active branches without these checks.
- `github_set_branch_auto_delete` configures GitHub's own
  `delete_branch_on_merge` repository setting. It requires repository admin
  permission and `confirm=true`. This is the preferred option for automatic
  cleanup of **all** merged branches, including branches merged outside this
  application; repository rules continue to apply.

For example, with a project that has already linked `OWNER/REPO`:

```sh
# Inspect checks and the SHA the next merge request must preserve.
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/github/pulls/42/verify?repository=OWNER/REPO"

# Merge only after the verification result reports ready_to_merge=true.
curl -fsS -X POST "$BASE/api/projects/research/github/pulls/42/merge?repository=OWNER/REPO" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"confirm":true,"expected_head":"REPLACE_WITH_40_CHARACTER_HEAD_SHA","merge_method":"squash","cleanup_branch":true}'
```

A single owner credential controls these operations; do not expose it to an
untrusted MCP client. PR and branch updates have preflight revision checks,
but GitHub does not offer atomic conditional deletion of a Git ref, so a small
race remains between the final reference read and delete. Repository protection
and rulesets are the final authority. For protected workflows, enable GitHub's
native automatic branch deletion and require reviews or merge queues as needed.
