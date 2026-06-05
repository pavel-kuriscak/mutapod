# mutapod — Architecture & Internals

This document is a starting-point reference for AI agents (and humans) working on
mutapod. It explains *what each piece does* and *why it does it that way*, so
you can navigate without re-reading every file. When details look outdated,
trust the code over this doc and update the doc.

---

## What mutapod is

A Go CLI that gives you a "cloud-backed local dev environment":

- code lives **locally**, edited with VS Code
- a **cloud VM** (GCP or Azure) runs the project's Docker Compose stack
- **Mutagen** continuously syncs files in both directions and forwards
  TCP ports between local and remote
- A project-scoped **Docker context** points VS Code at the remote daemon, so
  the Containers view, integrated terminal, and devcontainer attach all target
  the remote VM — without changing the user's globally active context

Module path: `github.com/mutapod/mutapod`. Single static binary.

---

## High-level design

Five non-obvious decisions shape the codebase:

1. **Pure-Go SSH for everything.** No PuTTY, no system `ssh`/`scp`, no
   `gcloud compute ssh` for non-interactive work. `golang.org/x/crypto/ssh` is
   used directly via the `internal/sshrun` package. This was added because
   PuTTY's host-key prompt blocked bootstrap on Windows. Cloud CLIs are still
   used for VM lifecycle (`gcloud compute ...` or `az vm ...`) and for
   interactive SSH. GCP uses `gcloud compute config-ssh`; Azure writes a
   project host alias itself after querying the VM IP.

2. **Mutagen for sync + port forwards, parsed as text.** Mutagen v0.18.1 has no
   `--output json` flag and no `--label-selector`. So `internal/sync/mutagen.go`
   uses session names + labels for identity and parses human-readable
   `mutagen sync list` / `mutagen forward list` output to determine status.
   Sessions are created once per workspace and *resumed* on subsequent runs;
   they are *paused* on `mutapod down` (history preserved for fast resume).

3. **Single Provider interface, but cloud CLIs are still shelled out.** Cloud
   ops happen through `gcloud` or `az`. The
   `Provider` interface (`internal/provider/provider.go`) hides this behind
   `EnsureInstance / State / SSHConfig / Exec / CopyFile / StopInstance /
   DeleteInstance`. Pure-Go SSH is reached through `Exec` and `CopyFile`.

4. **A remote compose override is generated on every `up`.** mutapod injects
   bind mounts for the synced workspace and for any active personal AI profile
   (codex/claude) into the primary service. The override is YAML, written to
   the remote workspace as `.mutapod.compose.override.yaml`, and `docker compose
   up` is invoked with both `-f compose.yaml -f .mutapod.compose.override.yaml`.
   This works even with compose files that weren't written for mutapod.

5. **Lease-based idle shutdown.** A small systemd timer + bash script
   (`internal/idle/scripts/`) runs on the VM. Each `mutapod up` writes a lease
   file (`/var/lib/mutapod/leases/<workspace>.lease`) with an expiry; while
   you're connected the local heartbeat refreshes it. `mutapod down` removes
   the lease. The remote checker stops the VM when *all* leases are expired,
   so multiple users sharing a VM don't surprise-stop each other.

---

## Package map

```
cmd/mutapod/main.go         entry point — calls cli.Execute()

internal/
  cli/                      cobra commands + the top-level "up/down" flows
    root.go                 root cmd, persistent flags (--config, --debug),
                            wiring of all subcommands, auto-update PreRun
    up.go                   the big one — orchestrates the entire `up` flow
    down.go (in up.go)      stops services, pauses sync, releases lease
    destroy.go              destroys VM + state, prompts for confirmation
    reset.go                terminate+delete+wipe state, then run `up`
    leases.go               `mutapod leases` — list VM-side lease records
    idle.go                 `mutapod idle-heartbeat` — runs in background
                            during VS Code usage to refresh the lease
    version.go              `version` and `update` commands
    autoupdate.go           prompt-on-launch update check (skipped for
                            certain subcommands and non-TTY runs)
    autoupdate_unix.go      syscall.Exec relaunch
    autoupdate_windows.go   no-op (Windows uses staged-replace pattern)

  config/                   parses mutapod.yaml; applies --provider override
                            before provider-specific defaults/validation;
                            helpers like cfg.WorkspacePath(),
                            cfg.InstanceName(), cfg.LocalSyncPath(),
                            cfg.Compose.*

  provider/                 Provider interface + registry
    gcp/gcp.go              GCP impl: gcloud + pure-Go SSH
    azure/azure.go          Azure impl: az + generated SSH config alias +
                            pure-Go SSH

  sshrun/                   pure-Go SSH client
                            — Run(ctx,cmd,stdin,stdout,stderr) with
                              keepalive@openssh.com every 30s
                            — Upload(local,remote) via `cat > '...'`
                            — TrustHost(known_hosts_file, alias) captures
                              the host key during a probe handshake and
                              writes a known_hosts line

  bootstrap/                installs docker + docker compose plugin on
                            the VM, hardens sshd. scripts/bootstrap.sh
                            is go:embedded into the binary.

  sync/                     Mutagen wrapper
    mutagen.go              Manager: workspace sync + forward + reverse
                            forward sessions, session config signature,
                            text-based status parsing, FlushSyncWithProgress
    sidecar.go              SidecarSession: per-profile sync sessions
                            (codex / claude / claude-homefile)

  ignore/                   .mutapodignore parsing → mutagen --ignore flags

  compose/                  detects compose file, parses ports, runs
                            `docker compose up/down` remotely; renders +
                            uploads the .mutapod.compose.override.yaml;
                            ExecInPrimaryService() runs scripts inside the
                            primary container; ConfigureGitSafeDirectory()
                            writes safe.directory='*' to /etc/gitconfig

  profiles/                 detects `codex` and `claude` CLIs locally, sets
                            up matching sidecar syncs + container-side
                            install scripts; handles the special
                            ~/.claude.json hard-link bridge

  agents/                   writes/updates AGENTS.md in the project root
                            (managed block delimited by HTML comments)

  vscode/                   generates mutapod.code-workspace, the attached-
                            container imageConfig (Dev Containers), and
                            launches VS Code in attached or local mode

  dockerctx/                creates/updates a project-scoped Docker context
                            named like the GCP instance, pointing at
                            ssh://<user>@<host>

  idle/                     remote idle-check installer (systemd
                            timer+service+script), lease writer, lease
                            lister; local heartbeat lock via gofrs/flock

  state/                    JSON state in ~/.mutapod/state/<name>.json:
                            instance ID, last known IP, sync session names,
                            forward session map, profile session list,
                            ignore signature, sync session config signature

  store/                    file-locked load/save for state.State

  shell/                    Commander interface (Run/Output) + a fake
                            for tests; debug streaming via
                            io.MultiWriter so output is shown AND captured

  deps/                     downloads mutagen into ~/.mutapod/bin/ if it
                            isn't on PATH

  buildinfo/                Version/Commit/Date set via -ldflags by GoReleaser

  update/                   GitHub release lookup, download, checksum
                            verify, install. On Linux/macOS replaces in
                            place. On Windows stages
                            `.mutapod.exe.<ts>.new` and starts a detached
                            CMD script that loops `move` until the running
                            binary is gone.
```

---

## The `mutapod up` flow

This is the heart of the tool. Each step in `internal/cli/up.go` calls into a
specialised package.

```
1.  parseUpLaunchMode(args)                  — "" or "container" → attached;
                                               "local" → open code-workspace
2.  loadConfig()                             — find mutapod.yaml walking up;
                                               provider.type is the default
                                               provider unless --provider
                                               overrides it
3.  confirmMissingIgnoreFile(...)            — warn if no .mutapodignore
4.  agents.Ensure(cfg)                       — write/update AGENTS.md block
5.  deps.MutagenPath()                       — download mutagen if needed
6.  state.Load(cfg.Name)                     — read ~/.mutapod/state/<n>.json
7.  provider.New(cfg, ...)                   — registry lookup → cloud Provider
8.  prov.EnsureInstance(ctx)                 — create or start VM
9.  prov.SSHConfig(ctx)                      — provider-specific SSH setup:
                                               GCP runs config-ssh and parses
                                               the generated entry; Azure
                                               queries VM IP and writes its own
                                               Host block; both TrustHost()
                                               for noninteractive access
10. profiles.Active(cfg)                     — detect codex/claude
11. bootstrap.Run(ctx, prov)                 — upload + run bootstrap.sh
12. ensureRemoteWorkspace(...)               — mkdir + chown /workspace/<n>
13. ensureRemoteProfilePaths(...)            — for each profile: mkdir +
                                               chown sync + tool dirs
14. mutagensync.DaemonStart(...)             — `mutagen daemon start`
15. EnsureSync                               — recreate if IP changed,
                                               ignore-rules changed, or
                                               session-config signature
                                               changed; otherwise resume
16. waitForInitialSync(...)                  — flush + verify ready + wait
                                               for the remote compose file
                                               to appear
17. Per-profile SidecarSession.Ensure        — same logic, plus the
                                               supplemental claude-homefile
                                               sync that bridges
                                               ~/.claude.json
18. removeRemoteWorkspaceWrapper(...)        — delete the .code-workspace
                                               file from the remote so it
                                               doesn't recurse
19. compose.EnsureRemoteOverride(...)        — render + upload
                                               .mutapod.compose.override.yaml
                                               (workspace bind + profile
                                               bind mounts)
20. compose.Up(...)                          — `docker compose up -d`,
                                               adding `-f override.yaml`
                                               when needed
21. ensureAttachedContainerWorkspaceACLs     — set ACLs so the attached
                                               container user can write
                                               into the synced workspace
22. compose.ConfigureGitSafeDirectory(...)   — `git config --system --add
                                               safe.directory '*'` inside
                                               the primary container
23. profiles.EnsureRemoteTools(...)          — runs each profile's setup
                                               script in the primary
                                               container (npm install of
                                               codex/claude CLI + wrapper
                                               at /usr/local/bin/<name>)
24. compose.ParsePorts + EnsureForward       — one mutagen forward per
                                               port, named mutapod-<n>-<p>
25. EnsureReverseForward (if configured)     — for cfg.Compose.ReverseForwards
26. state.Save(st)                           — persist everything
27. dockerctx.EnsureContext(...)             — create/update Docker context
                                               pointing at ssh://...
28. vscode.ConfigureWorkspace(...)           — generate
                                               mutapod.code-workspace
29. vscode.ConfigureAttachedContainer(...)   — generate the Dev Containers
                                               imageConfig JSON in user
                                               globalStorage
30. maybeConfigureIdle(...)                  — install systemd timer,
                                               write lease, start local
                                               heartbeat process
31. vscode.PrintInstructions + Launch(...)   — open VS Code (attached or
                                               local)
```

`mutapod down` is the inverse for state-changing steps:
- `compose.Down`
- `syncMgr.PauseSync`, pause each profile session
- `PauseAllForwards`, `PauseAllReverseForwards`
- if idle.enabled → release lease (the idle checker stops the VM later);
  otherwise stop the VM immediately

`mutapod destroy` confirms with the user, terminates *all* mutagen sessions,
deletes the GCP instance, removes the local Docker context, and wipes
`~/.mutapod/state/<name>.json`.

`mutapod reset` is `destroy` (without confirmation, scoped to the workspace)
followed by `up`.

---

## Auto-update on launch

`internal/cli/autoupdate.go` runs in `PersistentPreRun` on the root cobra
command. It is intentionally non-fatal:

- Skipped for: `version`, `update`, `idle-heartbeat`, `help`, `completion`
- Skipped when: `Version == "dev"` (un-released build), env
  `MUTAPOD_SKIP_UPDATE_CHECK=1`, or stdin/stdout are not TTYs
- Otherwise: 5s GitHub API check; if a newer release exists, prompt with a
  30s timeout
- On `y`: download → checksum check → install
  - Unix: `syscall.Exec(newPath, os.Args, os.Environ())` replaces the
    process transparently
  - Windows: the existing staged-replace pattern is used; the current
    invocation continues with the *old* binary, the next run picks up the
    new one

`internal/update/update.go` is the implementation: GoReleaser archive name
templates, sha256 checksums, archive-format-aware extraction (zip on
Windows, tar.gz on Unix), and `stageWindowsReplacement()` which writes a
detached CMD script that loops `move /Y` until the old process exits.

`update.go:26 repoOwner = "airguru"` — this is the real GitHub org; the Go
module path uses `mutapod/mutapod` for code organisation. They do not need
to match.

---

## State and persistence

Local (per-workspace, on the developer's machine):
- `~/.mutapod/state/<name>.json` — JSON state file (`internal/state/state.go`)
- `~/.mutapod/heartbeat/<name>.lock` — flock for the local heartbeat process
- `~/.mutapod/profile-links/claude-homefile/claude.json` — hard link target
  bridging `~/.claude.json` into a Mutagen-syncable directory
- `~/.mutapod/bin/mutagen` — auto-downloaded mutagen binary
- `~/.ssh/google_compute_engine` — gcloud-managed private key
- `~/.ssh/id_rsa` or configured Azure key — Azure SSH private key
- `~/.ssh/google_compute_known_hosts` — populated by `sshrun.TrustHost`
- `~/.ssh/known_hosts` — populated by `sshrun.TrustHost` for Azure aliases
- `~/.ssh/config` — populated by `gcloud compute config-ssh`; mutapod *parses*
  this for GCP and writes an Azure Host block itself

VM (per-instance, shared by anyone connecting to that VM):
- `/workspace/<name>` — synced project root
- `/workspace/<name>/.mutapod.compose.override.yaml` — generated override
- `/var/lib/mutapod/profiles/<profile>` — synced profile data
- `/var/lib/mutapod/profiles/claude-homefile/claude.json` — bridged
  ~/.claude.json
- `/var/lib/mutapod/tools/<profile>` — npm-installed CLI
- `/var/lib/mutapod/leases/<workspace>.lease` — lease record
- `/usr/local/bin/{codex,claude}` — mutapod-installed wrapper script
- `/usr/local/bin/mutapod-idle-check` — idle checker
- `/etc/systemd/system/mutapod-idle-check.{service,timer}`

Container (per `docker compose up`, ephemeral):
- `/root/.codex`, `/root/.claude`, `/root/.claude.json` — mutapod-injected
  bind mounts via the override
- `/etc/gitconfig` — `safe.directory '*'` written by mutapod after
  `compose up`

---

## Critical implementation gotchas

These are the issues that took live testing to find. Future debugging usually
ends up here.

| Topic | Detail |
|-------|--------|
| **Pure-Go SSH keepalives** | Long silent commands (`apt-get install nodejs npm`, `npm install -g`) silently dropped their TCP connection through GCP NAT, surfacing as `ssh.ExitMissingError` ("remote command exited without exit status or exit signal"). `sshrun.Run` sends `keepalive@openssh.com` every 30s while a session is active. |
| **GCP HostKeyAlias** | gcloud writes `HostKeyAlias=compute.NNNNNN` (with `=`, not space) into `~/.ssh/config`. The parser must accept either separator and trim ` \t=` from the value. The alias is mandatory for mutagen, since mutagen calls into ssh which verifies the key against the known_hosts entry under that alias. |
| **`google_compute_known_hosts` directory bug** | An earlier bug created the file as a *directory* via `os.MkdirAll(knownHostsFile, ...)`. Fix: `os.MkdirAll(filepath.Dir(knownHostsFile), 0700)`. Verify before writing. |
| **GCP SSH username** | gcloud doesn't honour the `remote_user` from mutapod.yaml — it provisions keys for the *local* OS user (lowercased, with `DOMAIN\` stripped on Windows). `gcpSSHUsername()` derives this; the parsed `User` from `~/.ssh/config` takes precedence when present. |
| **Azure private-only default** | Azure VM creation passes `--public-ip-address "" --nsg-rule NONE` unless `provider.azure.public_ip: true` is set. SSH uses the VM private IP by default, so the local machine must have private routing to the target subnet. |
| **Azure SSH alias** | Azure does not need `az ssh config` for mutapod's noninteractive path. `provider/azure` writes `Host <instance>.azure` with `HostName`, `IdentityFile`, `UserKnownHostsFile`, and `HostKeyAlias`, then trusts the host key in `~/.ssh/known_hosts`. |
| **Mutagen text parsing** | v0.18.1 has no JSON output. `parseSyncStatus` and `parseForwardStatus` walk the human-readable lines looking for `Status:` and normalise to a fixed token set. `isNoSessions(err)` recognises the three different "not found" phrasings mutagen has used. |
| **Session config signature** | `Manager.SessionConfigSignature` hashes the args mutagen would create the session with. When the signature differs from the saved one, the session is terminated and recreated. The version prefix (`v3` currently) is bumped whenever the args list changes structurally; this forces a one-shot recreation on upgrade. |
| **Stale-IP recreation** | When the VM IP changes between runs, mutagen sessions encoded with the old endpoint are terminated and recreated rather than resumed. `state.Instance.LastKnownIP` is the source of truth for the comparison. |
| **Hard-linked `~/.claude.json`** | Mutagen syncs *directories*, not single files. A hard link at `~/.mutapod/profile-links/claude-homefile/claude.json` (same inode as `~/.claude.json`) lets the supplemental sync session sync just that file as if it were in its own directory. The remote bind-mounts the bridged file at `/root/.claude.json`. |
| **`io.MultiWriter` in debug mode** | When `--debug` is on, `shell.Run` streams stdout/stderr to the user *and* captures it for error inspection. Without this, error matchers like `isNotFound(err)` saw an empty error message and broke. |
| **Windows binary self-replacement** | A running `mutapod.exe` cannot be overwritten. The updater stages the new binary as `.mutapod.exe.<ts>.new` next to it and launches a detached `cmd.exe /C start /B script.cmd` that retries `move /Y` until the old process exits. The current run continues with the old version; next invocation gets the new one. |
| **Profile-only `--ignore-vcs`** | The main workspace sync now syncs `.git` (so VS Code shows the branch and remote agents can read history). Sidecar sessions still pass `--ignore-vcs` selectively. The signature was bumped to `v3` so existing sessions are recreated automatically. |
| **`safe.directory` in container** | After `docker compose up`, mutapod runs `git config --system --add safe.directory '*'` inside the primary container so VS Code stops complaining about "dubious ownership" when the bind-mounted file UIDs don't match the container user. |
| **Compose override is detected, not assumed** | `NeedsRemoteOverride` returns true only when there's a workspace-mount need (cfg specifies WorkspaceFolder + the base compose file doesn't already mount it) or any active profile has mounts. Otherwise no override file is generated and `docker compose` runs with just the original `-f`. |

---

## Build & release

- `go build ./cmd/mutapod` → `mutapod.exe` in repo root (gitignored).
- `.goreleaser.yaml` builds linux/darwin (amd64+arm64) and windows/amd64.
- `.github/workflows/release.yaml` triggers on tag push (`v*`), runs
  goreleaser, publishes a GitHub Release with archives + `checksums.txt`.
- Windows zip ships with `install.bat` (from `scripts/install.bat`) which
  copies `mutapod.exe` to `%LOCALAPPDATA%\mutapod\bin` and adds it to the
  user PATH (no admin needed).
- Releasing: `git tag vX.Y.Z && git push origin main && git push origin vX.Y.Z`.

---

## Running tests

```bash
go test ./...
```

Tests use `internal/shell.FakeCommander` to assert on the exact arg lists
mutapod would shell out with. Live cloud testing is done from `testproject/`
(itself gitignored) with a real `mutapod.yaml`.

---

## Things deliberately *not* in the codebase

- A user-config file. Settings live in `mutapod.yaml`, environment, and
  `~/.mutapod/state/`. No `~/.mutapodrc`.
- A daemon process. The local heartbeat is a child process spawned by `up`
  and killed on `down`; everything else is one-shot CLI invocations.
- A plugin system. Profiles are hardcoded (codex, claude). Adding a third
  profile means another entry in `internal/profiles/profiles.go`.
- Direct API SDKs. Cloud ops use the cloud CLI; this kept the binary small
  and re-used the user's existing cloud auth (`gcloud auth login` or
  `az login`).
