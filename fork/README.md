# Clean fork generation

This directory is the additive boundary for fork builds and deployments. Product capabilities still require a bounded set of upstream-tree integration hooks; [`UPSTREAM_INVASION.md`](UPSTREAM_INVASION.md) inventories those paths and their overlap with later upstream changes. The source generation starts at the exact SHA in `UPSTREAM_BASELINE`, while `UPSTREAM_BASELINE_TAG` pins the complete-tag identity used for version derivation. Prior fork branches are evidence only and are never merged or cherry-picked.

Official upstream Compose and Docker files remain untouched by target policy. Build and run the fork by layering these files after the upstream self-host Compose file:

```bash
export FORK_BACKEND_IMAGE=multica-fork-backend:<immutable-tag>
export FORK_WEB_IMAGE=multica-fork-web:<immutable-tag>
docker compose -f docker-compose.selfhost.yml -f fork/compose.yml config
```

For a local source build, also add `fork/compose.build.yml`. `fork/scripts/verify-source.sh` rejects dirty trees, merge commits after the frozen upstream base, and versions that cannot be traced to a Git SHA. `fork/scripts/build-images.sh` builds one architecture at a time and verifies both images carry the exact source revision label. The optional `FORK_GOPROXY` environment variable is forwarded only to the backend builder; its default remains `https://proxy.golang.org,direct`.

The web build uses Fontsource package assets instead of `next/font/google`. The runtime image ships the corresponding SIL OFL 1.1 license files under `/usr/share/licenses/multica-fonts`.

Host CLI deployment is a separate transaction. After the target containers pass readiness, `fork/scripts/install-cli-transaction.sh` checks that the selected profile is idle and authenticated, retains the previous command, atomically activates the immutable candidate, restarts the daemon, and verifies the running process image reports the same full commit. Mini uses profile `mini`; the `imile-win` host retains four distinct profiles: `local`, `mini`, `imile-win.fed`, and the unnamed default profile.

The deployed generation is [`releases/v0.11.1-main.20261121.md`](releases/v0.11.1-main.20261121.md), based on frozen upstream main `e293fca0d3b753f470a6de21c256eae822b4dc34`. Runtime source is `f5943de02e0b56bd9bbf23c50fbc7c2f75716a24`, version `v0.11.1-64-gf5943de02`. Image tags use the generation-specific prefix `multica-fork-v0111-20261121`. Source acceptance, each target deployment, and CLI activation have separate receipts. Historical manifests may predate a later rollout; the target image revision and CLI process image establish what is actually running. The formal branch's final documentation commit is not a new runtime build.

## Build identity

Current upstream separates the CLI display commit (`main.commit`) from daemon handshake metadata (`main.buildCommit`). Both must be the full source SHA. A correct `--version` alone does not prove daemon identity. The deployed host CLIs were built locally from clean, CI-accepted `f5943de02` with Go 1.27.4 and these linker inputs:

```bash
-ldflags "-X main.version=$version -X main.commit=$source_sha -X main.buildCommit=$source_sha -X main.date=$timestamp"
```

Before activation, verify both fields in `go version -m`, the target platform, and the binary checksum. After activation, verify the real process image and daemon `cli_commit`, UUID, device name, and server. Keep immutable artifacts with different bytes in different build directories, even when the source SHA and display version match.

The build helpers at runtime commit `f5943de02` omit `main.buildCommit`. Do not use their unmodified CLI build command for another host activation. This release's corrected local build commands and checksums are recorded in its manifest. A separately versioned build-recipe correction must pass its own CI before being used for a future release; it is not a documentation-only change and is not part of the deployed source. The CLI embedded in the current backend image is not used by any deployed daemon.

## Target network and backup policy

The Mini–imile-win Tailscale path uses a peer relay on the metered `txx` host. Keep large-file transport off this path. Build on the target when practical, or have that target download the immutable artifact directly from its artifact provider. Verify the artifact digest and image identities locally. SSH between these hosts is for small control commands and redacted receipts, not image archives, CLI binaries, or database dumps.

Create the frozen database backup and its independent restore on the same target. Use a fresh, isolated local verification container and volume; keep the backup, restored data, and receipt local. Disk pressure should lead to a deliberate build/cache placement decision, not an automatic cross-host transfer. The generic deployment script's historical SSH transport is not the chosen route for these two targets. This generation used reviewed, target-local operators whose hashes and restore proofs are recorded in the release manifest.

Capture External PR census before deployment and compare the post-deployment census with `--baseline <path>`. The comparison uses exact row identities and recorded state instead of historical row-count constants. CLI activation accepts `--device-name`, retains the same daemon UUID, and backs up the profile config alongside the previous executable before changing it.

Rollback after a database migration requires restoring the frozen dump into a fresh database and verifying it before switching database names with the application stopped. Retain the upgraded database under a unique archive name. `pg_restore --clean` into the upgraded database leaves new objects absent from the old dump behind; a zero exit status does not prove a complete rollback. Pre-switch task counts must be zero. Work legitimately claimed after the new daemon starts is recorded in its receipt and does not by itself trigger rollback.
