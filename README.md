# mutapod

`mutapod` provisions a remote VM, syncs your local project to it with Mutagen, runs `docker compose` on the VM, forwards ports back to your machine, and generates a project-scoped VS Code workspace that points Docker-aware tooling at the remote engine.

The intended workflow is:

- edit code locally
- run services remotely
- open the generated `mutapod.code-workspace` in VS Code so the Containers view and integrated terminal talk to the remote Docker daemon for this project only
- let mutapod manage a project-scoped Docker context without changing your global active context

## Current Status

GCP and Azure are supported providers today.

## Commands

- `mutapod up`: create or start the VM, sync files, run `docker compose up`, forward ports, configure VS Code workspace integration, start lease tracking, and open VS Code attached to the main container by default
- `mutapod up local`: same as `mutapod up`, but open the local `mutapod.code-workspace` instead of the attached-container window
- `mutapod up --build`: same as `mutapod up`, but force `docker compose` to rebuild images before starting services
- `mutapod up --replace`: approve recreation when VM-facing YAML changed
- `mutapod up --adopt`: mark an existing legacy VM as matching the current YAML without recreating it
- `mutapod reset`: terminate local sync state, delete the current VM, clear local mutapod state, and run `mutapod up` again from scratch
- `mutapod down`: run `docker compose down`, pause sync and forwards, release this workspace lease, and stop the VM immediately only when idle shutdown is disabled
- `mutapod destroy`: destroy the VM after an explicit confirmation prompt, warn if other workspace leases are visible on the VM, and clean up local mutapod state
- `mutapod status`: show the current workspace, provider, VM, and sync state
- `mutapod version`: show the installed mutapod version and build metadata
- `mutapod update --check`: check GitHub Releases and report whether a newer mutapod release is available
- `mutapod update`: download the latest GitHub release for your platform, verify its checksum, and replace the local mutapod binary
- `mutapod ssh`: open an interactive shell on the remote VM
- `mutapod leases`: show the VM-side mutapod lease records, including last heartbeat and expiry

## Quick Start

1. Install the cloud CLI for your provider: `gcloud` for GCP or `az` for Azure.
2. Authenticate the CLI for the target project/subscription.
3. Install Docker locally.
4. Add a `mutapod.yaml` to your project.
5. Run `mutapod up`, or choose a provider explicitly with `mutapod --provider azure up`.
6. If you prefer the local workspace wrapper instead of attached-container mode, run `mutapod up local`.
7. If you need a fresh image rebuild, use `mutapod up --build`.

Mutagen is downloaded automatically into `~/.mutapod/bin` if it is not already available on `PATH`.
mutapod itself can be updated explicitly with `mutapod update`.

## Example `mutapod.yaml`

GCP:

```yaml
name: myapp

provider:
  type: gcp
  gcp:
    project: my-gcp-project
    zone: europe-west4-a

compose:
  file: compose-dev.yaml
  primary_service: web
  workspace_folder: /app
  extra_ports: [8080]

idle:
  enabled: true
  timeout_minutes: 30
  check_interval_seconds: 60
```

Azure:

```yaml
name: myapp

provider:
  type: azure
  azure:
    subscription: 00000000-0000-0000-0000-000000000000
    resource_group: rg-dev
    location: westeurope
    vm_size: Standard_D4s_v5

compose:
  file: compose-dev.yaml
  primary_service: web
  workspace_folder: /app
  extra_ports: [8080]
```

## Config Reference

### `name`

Workspace name. This is required.

It is used for:

- the default remote workspace path: `/workspace/<name>`
- the current cloud instance name, together with your active account token
- Mutagen session names
- local state file naming
- lease file naming on the VM

At the moment, this means one workspace name maps to one VM per account.

## Declarative VM Configuration

mutapod treats the VM-facing part of `mutapod.yaml` as declarative. It stores a
versioned fingerprint on each VM and checks it on every `mutapod up`.

- GCP stores the fingerprint in the reserved `mutapod-config` instance label.
- Azure stores it in the reserved `mutapod-config` VM tag.
- If the fingerprint matches, a running VM is reused and a stopped VM is started.
- If VM-facing YAML changed, mutapod recreates the VM rather than applying a
  provider-specific mix of in-place updates.
- Interactive runs prompt with `Recreate VM? [y/N]`.
- Noninteractive runs must use `mutapod up --replace` to approve required replacement.

Recreation permanently deletes the VM and anything stored only on its disk.
Project files should remain authoritative on the local machine and sync back to
the new VM.

VMs created by older mutapod versions have no fingerprint. On the first `up`,
either recreate the VM or use `mutapod up --adopt` to stamp the current
configuration without verifying the live VM properties. `--adopt` is accepted
only for an untracked legacy VM; it cannot override a known configuration
mismatch.

The fingerprint detects changes to normalized YAML, including VM size, image,
disk, network, identity/service account, cloud tags or labels, and other
creation settings. Ordering-only changes to maps and set-like lists do not
trigger replacement. It does not inspect arbitrary changes made manually in
the cloud portal or CLI, and movement of an image alias does not change the
fingerprint unless the configured alias changes.

Cloud target selectors are handled separately:

- GCP: `project` and `zone`
- Azure: `tenant`, `subscription`, and `resource_group`

Changing the active provider or target scope causes mutapod to manage the VM in
the new target. The last-known VM in the previous target is preserved and its
resource ID is printed as a warning; mutapod never deletes across cloud scopes
automatically.

All explicit deletion prompts, including `destroy`, `reset`, and declarative
replacement, use a simple `[y/N]` confirmation. Existing `--yes` flags on
`destroy` and `reset` still bypass their prompts.

### `provider`

`provider.type` is the default provider and must be `gcp` or `azure` when set. If no `--provider` flag is supplied, `provider.type` is required. The active provider for a run can be overridden with `mutapod --provider gcp ...` or `mutapod --provider azure ...`.

This means a project can keep both provider-specific sections in one `mutapod.yaml` and select the actual target from the command line:

```yaml
provider:
  type: gcp
  gcp:
    project: my-gcp-project
  azure:
    subscription: 00000000-0000-0000-0000-000000000000
    resource_group: rg-dev
```

Then:

- `mutapod up` uses the YAML default provider, here `gcp`
- `mutapod --provider azure up` uses the Azure section for this invocation
- `mutapod --provider gcp status` checks the GCP VM even if another default is set

#### `provider.gcp`

These settings apply when `provider.type: gcp`.

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `project` | yes | none | GCP project ID |
| `zone` | no | `us-central1-a` | Compute Engine zone |
| `machine_type` | no | `e2-standard-4` | VM machine type |
| `disk_size_gb` | no | `30` | Boot disk size in GB |
| `disk_type` | no | `pd-balanced` | Boot disk type |
| `image_family` | no | `ubuntu-2204-lts` | Base image family |
| `image_project` | no | `ubuntu-os-cloud` | Image project |
| `network` | no | empty | Passed through to `gcloud compute instances create --network` |
| `subnet` | no | empty | Passed through to `--subnet` |
| `service_account` | no | empty | Passed through to `--service-account` |
| `tags` | no | empty | Passed through as repeated `--tags` values |
| `preemptible` | no | `false` | Uses preemptible instances unless `spot` is set |
| `spot` | no | `false` | Uses `--provisioning-model SPOT` |
| `labels` | no | `managed-by=mutapod` | GCP instance labels; `mutapod-config` is reserved |

For GCP, the SSH login user is derived from `gcloud compute config-ssh` and your local setup. It is intentionally not part of the normal `mutapod.yaml` workflow.

For GCP VM naming, mutapod uses the active account from `gcloud config get-value account`, takes the local part before `@`, and sanitizes it into a cloud-safe token. For example, `pavel.kuriscak@gmail.com` becomes `pavel-kuriscak`, so a workspace named `myapp` becomes `mutapod-pavel-kuriscak-myapp`.

#### `provider.azure`

These settings apply when `provider.type: azure`.

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `resource_group` | yes | none | Azure resource group containing the VM |
| `subscription` | no | active Azure CLI subscription | Passed through to `az --subscription` for VM operations |
| `tenant` | no | empty | Accepted for config clarity; sign in with `az login --tenant ...` before running mutapod |
| `location` | no | resource group's location | Passed to `az vm create --location` when set |
| `vm_size` | no | `Standard_D4s_v5` | Azure VM size |
| `disk_size_gb` | no | `30` | OS disk size in GB |
| `storage_sku` | no | `StandardSSD_LRS` | OS disk storage SKU |
| `image` | no | `Ubuntu2204` | Azure CLI image alias, URN, or image ID |
| `vnet` | no | Azure CLI default | Passed through to `az vm create --vnet-name` |
| `subnet` | no | Azure CLI default | Passed through to `--subnet` |
| `admin_username` | no | `azureuser` | Local Linux user used for SSH |
| `identity` | no | empty | Passed through to `--assign-identity`; use `[system]` for system-assigned identity |
| `public_ip` | no | `false` | Create a public IP and SSH NSG rule. By default Azure VMs are private-only |
| `public_ip_sku` | no | `Standard` | Passed through to `--public-ip-sku` only when `public_ip: true` |
| `prefer_private_ip` | no | `false` | Prefer the VM private IP even when `public_ip: true` |
| `ssh_sources` | no | empty | Private CIDR ranges allowed to SSH to the VM through the generated NSG |
| `ssh_private_key_file` | no | `~/.ssh/id_rsa` | Private key used by mutapod's pure-Go SSH client |
| `ssh_public_key_file` | no | matching `.pub` or generated by Azure CLI | Public key passed to `az vm create --ssh-key-values` |
| `tags` | no | `managed-by=mutapod` | Azure VM tags; `mutapod-config` is reserved |

For Azure, mutapod creates private-only VMs by default with `az vm create --public-ip-address "" --nsg-rule NONE`. Your machine must be able to reach the VM private IP over VPN, ExpressRoute, peering, or another private network path. Set `public_ip: true` only in environments where public IPs are allowed.

If Azure CLI creates a VM NSG and your VPN clients are not covered by Azure's default `VirtualNetwork` source, set `ssh_sources`, for example:

```yaml
provider:
  azure:
    ssh_sources:
      - 10.130.1.0/27
```

The managed `mutapod-ssh` NSG rule is reconciled on every Azure `up`: mutapod
creates it when missing, updates its configured source ranges, and removes it
when `ssh_sources` is empty.

mutapod uses Azure CLI for VM lifecycle operations and writes its own OpenSSH host alias in `~/.ssh/config` for Mutagen. Non-interactive bootstrap and compose work use the same pure-Go SSH client as GCP. Interactive `mutapod ssh` delegates to `az ssh vm` so terminal handling stays normal.

Azure VM naming mirrors GCP: mutapod reads the active account from `az account show --query user.name --output tsv`, takes the local part before `@`, and sanitizes it into a cloud-safe token.

If your Azure access is controlled by Privileged Identity Management (PIM), activate the required Azure resource role before any operation that reads, creates, starts, deallocates, or deletes the VM. Once the VM exists and is reachable over SSH, normal sync/bootstrap/compose work does not need Azure control-plane permissions unless mutapod has to start, stop, inspect, or destroy the VM.

### `sync`

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `local_path` | no | `.` | Local directory to sync, relative to the config file if not absolute |
| `remote_path` | no | `/workspace/<name>` | Remote project directory |
| `mode` | no | `two-way-resolved` | Passed to Mutagen as `--sync-mode` |

For the common case, you can omit `sync` entirely. The defaults already mean:

- sync the current project directory
- place it at `/workspace/<name>` on the VM
- use Mutagen `two-way-resolved` mode

### `compose`

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `file` | no | auto-detect | Relative path to the Compose file. If omitted, mutapod looks for `compose.yaml`, `compose.yml`, `docker-compose.yaml`, or `docker-compose.yml` |
| `primary_service` | no | empty | Service name to highlight in post-`up` Dev Containers attach instructions |
| `workspace_folder` | no | inferred when possible | In-container project path for the generated "Attach to Running Container" profile. If omitted, mutapod first tries to infer it from the primary service bind mount or `working_dir` |
| `extensions` | no | empty | Extra VS Code extension IDs to add to the generated attached-container profile |
| `copy_local_extensions` | no | `true` | Copies your current local `code --list-extensions` set into the generated attached-container profile |
| `extra_ports` | no | empty | Additional ports to forward besides those discovered from the Compose file |
| `reverse_forwards` | no | empty | Local machine ports to expose on the remote VM and into the primary container via `host.docker.internal:<port>` |

`docker compose` is executed remotely from the synced workspace directory.

When `compose.primary_service` and `compose.workspace_folder` are set, mutapod automatically injects a compose override on the remote VM to bind-mount the synced workspace into that service if the base compose file doesn't already do it. This is what enables the intended "edit locally, sync to VM, see changes live in the remote container" workflow even for repos whose compose file wasn't written specifically for mutapod.

When `compose.reverse_forwards` is set, mutapod creates reverse Mutagen forwards from the remote VM back to your local machine and injects `host.docker.internal:host-gateway` into the primary service automatically. This lets code inside the main container reach local services on your machine using the same port numbers through `host.docker.internal:<port>`.

By default, `mutapod up` runs:

- `docker compose up -d`

If you want to force an image rebuild for that run, use:

- `mutapod up --build`
- `mutapod up local --build`

This is also how you point mutapod at a dedicated development stack, for example:

```yaml
compose:
  file: compose-dev.yaml
  primary_service: web
  workspace_folder: /app
```

That works well for setups where `compose-dev.yaml` starts multiple dev-only services such as:

- a web container where you manually run `python manage.py runserver`
- a webpack container running in watch mode

When `primary_service` is set, `mutapod up` also pre-generates the VS Code attached-container profile for that service. By default this profile:

- uses `workspace_folder` when set
- otherwise tries to infer the folder from the service bind mount or `working_dir`
- copies your currently installed local VS Code extensions
- merges in any extra IDs from `compose.extensions`

### `profiles`

`profiles` lets mutapod detect selected personal AI-agent setups on your machine, sync their data outside the repo workspace, and bootstrap matching CLIs inside the primary container automatically.

In the normal case, you do not need to configure this section at all.

If `codex` or `claude` is installed locally and available on your `PATH`, mutapod will:

- detect it automatically
- sync its local home directory if present
- mount that data into the primary container
- install the matching CLI inside the primary container automatically on `mutapod up`

Supported keys today:

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `codex.enabled` | no | auto-detect | If omitted, enabled when `~/.codex` exists locally |
| `codex.local_path` | no | `~/.codex` | Override the local Codex home to sync |
| `codex.remote_path` | no | `/var/lib/mutapod/profiles/codex` | Remote VM directory used as the sync target |
| `codex.mount_path` | no | `/root/.codex` | Container mount path |
| `claude.enabled` | no | auto-detect | If omitted, enabled when `~/.claude` exists locally |
| `claude.local_path` | no | `~/.claude` | Override the local Claude home to sync |
| `claude.remote_path` | no | `/var/lib/mutapod/profiles/claude` | Remote VM directory used as the sync target |
| `claude.mount_path` | no | `/root/.claude` | Container mount path |

Behavior:

- mutapod auto-enables Codex when local `codex` is installed
- mutapod auto-enables Claude Code when local `claude` is installed
- mutapod creates extra Mutagen sync sessions for the enabled profiles when local profile data exists
- these syncs are separate from the project workspace sync
- Codex SQLite runtime databases such as `logs_*.sqlite`, `state_*.sqlite`, `goals_*.sqlite`, and `memories_*.sqlite` are not synced, because Codex may rewrite or validate them differently across platform-specific extension binaries
- when `compose.primary_service` is set, mutapod mounts the synced profile directories into that service through the generated remote compose override
- mutapod also creates a persistent tool directory on the VM for each active profile and installs the corresponding CLI in the primary container automatically
- the Codex CLI wrapper exports `CODEX_HOME` to the mounted profile data for terminal use; the VS Code Codex extension is left to use its bundled CLI
- Claude Code is launched through a wrapper that gives it a stable managed `HOME`, so its `~/.claude` data comes from the mounted profile automatically

Current limitations:

- GitHub Copilot state is not part of this first pass
- the automatic remote install currently targets Node-based Codex and Claude Code CLIs
- if your primary container image cannot install Node.js/npm with `apt`, `apk`, `dnf`, or `yum`, mutapod will fail during profile bootstrap and you will need a project-specific container image setup

### `idle`

| Key | Required | Default | Notes |
| --- | --- | --- | --- |
| `enabled` | no | `true` | Enables VM-side idle shutdown enforcement |
| `timeout_minutes` | no | `30` | Lease lifetime and idle timeout window |
| `check_interval_seconds` | no | `60` | Heartbeat interval and systemd timer cadence |

Important behavior:

- mutapod now writes VM-side lease records even when `idle.enabled: false`
- when `idle.enabled: true`, the VM is stopped by the remote idle checker only after all leases expire
- when `idle.enabled: false`, leases are still useful for visibility through `mutapod leases`, but `mutapod down` stops the VM immediately after releasing this workspace lease

Lease records live on the VM under `/var/lib/mutapod/leases`.

## VS Code Integration

`mutapod up` generates `mutapod.code-workspace` in the project root and now opens VS Code attached to the main container by default.

If you want the local workspace wrapper instead, run:

- `mutapod up local`

`mutapod up` also creates or updates a named Docker context for the workspace. The generated workspace then points VS Code at that context and keeps terminal access aligned with it.

The generated workspace currently sets a project-scoped Docker context for:

- the Containers extension environment
- the integrated terminal on Windows
- the integrated terminal on Linux
- the integrated terminal on macOS

It also writes `docker.context` and keeps a matching `docker.host` value available for extensions that still consult the host directly.

This keeps the editor local while Docker operations target the remote VM.

## Ignore Rules

If a `.mutapodignore` file exists in the project root, mutapod converts it into Mutagen ignore rules for the sync session.

`mutapod.code-workspace` is also ignored automatically so the local workspace wrapper does not get synced into the remote container and show up as a recursive workspace suggestion during attach.

Important details:

- `.gitignore` is not used as a Mutagen sync rule source.
- VCS directories such as `.git` are synced by default, so the remote container and any agents running there can see the current branch and history. Add entries to `.mutapodignore` if you want to exclude specific subpaths (e.g. `.git/objects/pack`).
- mutapod creates sync sessions with Mutagen `--no-global-configuration` so your per-user `~/.mutagen.yml` cannot silently override project sync behavior.

## Notes

- Mutagen sync and forward sessions are reused when possible for fast restarts.
- The VM-side lease registry is the source of truth for shared usage checks.
- The current VM naming model is one VM per workspace name per account token.
