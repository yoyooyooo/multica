# Local fork runtime

## Scope

This Standard owns the local-machine operating model for repository owners who intentionally run the Multica fork end to end:

```text
fork-built user-global Multica CLI
  -> the same binary starts the local daemon
  -> the daemon connects to the fork-built Docker backend
  -> the web surface comes from the fork-built Docker frontend
```

It does not replace the public CLI installation guide. It applies only to an owner-controlled machine where the fork is the intended runtime authority.

## One binary, two roles

The daemon is not a separately built executable. `multica daemon start` resolves the currently invoked `multica` executable and starts that same binary as:

```text
multica daemon start --foreground --profile <profile>
```

Therefore the user-global command and the daemon must be upgraded together. A daemon health response proves the running daemon version; `multica --version` alone proves only the command currently resolved by the shell.

## Global command authority

The fork CLI is installed under the user-owned prefix:

```text
~/.local/bin/multica
```

`~/.local/bin` must precede Homebrew or another package manager in `PATH`. An official package-manager installation may remain as an explicit fallback, but it is not the normal command authority on this machine.

The tracked installer activates an immutable source-specific binary through that stable command:

```bash
make install-local-fork-cli PROFILE=mini
```

Equivalent direct entry:

```bash
scripts/install-local-fork-cli.sh --profile mini
```

Use plan mode to inspect source, version, profile, and paths without building or changing runtime state:

```bash
make install-local-fork-cli-plan PROFILE=mini
```

## Installer contract

The installer:

1. requires a clean Git worktree and parser-compatible `git describe` version;
2. refuses replacement while the selected daemon reports active tasks;
3. builds `server/cmd/multica` from the current exact source;
4. stores the binary under `~/.local/lib/multica/<commit>-<version>/multica`;
5. uses the candidate binary to verify that the selected profile can authenticate to its configured Server before changing the global command or running daemon;
6. preserves the prior global command under `~/.local/lib/multica/backups/`;
7. atomically switches `~/.local/bin/multica` to the new immutable binary;
8. persists `disable_auto_update=true` for the selected profile so the official release updater cannot replace fork authority;
9. restarts the selected daemon using the activated binary;
10. requires daemon health to report both `status=running` and the exact built version;
11. restores the previous global command and attempts to restart it if activation verification fails.

The command installs only CLI/daemon bytes. It does not rebuild or switch Docker backend/frontend images.

## Verification

After installation:

```bash
command -v multica
multica --version
multica --profile mini config show
multica --profile mini daemon status --output json
ps -p "$(cat ~/.multica/profiles/mini/daemon.pid)" -o command=
```

Expected properties:

- `command -v multica` resolves `~/.local/bin/multica`;
- the CLI version equals the clean source build version;
- `disable_auto_update` is `true` for the selected profile;
- daemon status is `running` and `cli_version` equals the same source build version;
- the daemon command points through the user-global fork CLI or its immutable target.

For an end-to-end same-source deployment claim, separately verify Docker image revision labels, image digests, backend `/readyz`, frontend configuration, storage/network preservation, and the applicable deployment receipt. CLI/daemon equality does not prove backend/frontend equality.

## Rollback boundary

The automatic rollback covers only CLI activation and daemon restart. It does not roll back:

- Docker images;
- database migrations;
- uploaded files;
- workspace/runtime state;
- a forward-only generation boundary.

To roll back CLI/daemon manually, confirm zero active tasks, reactivate a retained binary from `~/.local/lib/multica/backups/`, restart the daemon, and verify its reported version. Runtime rollback across database or image changes must follow the generation's deployment receipt and the [Fork Development Standard](fork-development.md).

## Freshness

Update this Standard when any of these contracts change:

- CLI build target or version admission;
- daemon self-executable behavior;
- profile configuration or auto-update precedence;
- user-global install path;
- daemon health fields used for verification;
- fork deployment or rollback authority.
