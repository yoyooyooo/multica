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

Host CLI deployment is a separate transaction. `fork/scripts/build-cli.sh` cross-builds a candidate from a clean exact SHA and records its checksum. After the target containers pass readiness, `fork/scripts/install-cli-transaction.sh` checks that the selected profile is idle and authenticated, retains the previous command, atomically activates the immutable candidate, restarts the daemon, and verifies the running process image reports the same full commit. Mini uses profile `mini`; the `imile-win` host uses profile `local`.

The current candidate is [`releases/v0.11.1-main.20261121.md`](releases/v0.11.1-main.20261121.md), based on frozen upstream main `e293fca0d3b753f470a6de21c256eae822b4dc34`. Image tags use the generation-specific prefix `multica-fork-v0111-20261121`. Source acceptance, each target deployment, and CLI activation require separate receipts. Historical manifests may predate a later rollout; the target image revision and CLI process image establish what is actually running.

Capture External PR census before deployment and compare the post-deployment census with `--baseline <path>`. The comparison uses exact row identities and recorded state instead of historical row-count constants. CLI activation accepts `--device-name`, retains the same daemon UUID, and backs up the profile config alongside the previous executable before changing it.

Rollback after a database migration requires restoring the frozen dump into a fresh database and verifying it before switching database names with the application stopped. Retain the upgraded database under a unique archive name. `pg_restore --clean` into the upgraded database leaves new objects absent from the old dump behind; a zero exit status does not prove a complete rollback. Pre-switch task counts must be zero. Work legitimately claimed after the new daemon starts is recorded in its receipt and does not by itself trigger rollback.
