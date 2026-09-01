# Clean fork generation

This directory is the additive boundary for fork builds and deployments. The source generation starts at the exact SHA in `UPSTREAM_BASELINE`; prior fork branches are evidence only and are never merged or cherry-picked.

Official upstream Compose and Docker files remain untouched by target policy. Build and run the fork by layering these files after the upstream self-host Compose file:

```bash
export FORK_BACKEND_IMAGE=multica-fork-backend:<immutable-tag>
export FORK_WEB_IMAGE=multica-fork-web:<immutable-tag>
docker compose -f docker-compose.selfhost.yml -f fork/compose.yml config
```

For a local source build, also add `fork/compose.build.yml`. `fork/scripts/verify-source.sh` rejects dirty trees, merge commits after the frozen upstream base, and versions that cannot be traced to a Git SHA. `fork/scripts/build-images.sh` builds one architecture at a time and verifies both images carry the exact source revision label. The optional `FORK_GOPROXY` environment variable is forwarded only to the backend builder; its default remains `https://proxy.golang.org,direct`.

The web build uses Fontsource package assets instead of `next/font/google`. The runtime image ships the corresponding SIL OFL 1.1 license files under `/usr/share/licenses/multica-fonts`.

Host CLI deployment is a separate transaction. `fork/scripts/build-cli.sh` cross-builds a candidate from a clean exact SHA and records its checksum. After the target containers pass readiness, `fork/scripts/install-cli-transaction.sh` checks that the selected profile is idle and authenticated, retains the previous command, atomically activates the immutable candidate, restarts the daemon, and verifies the running process image reports the same full commit. Mini uses profile `mini`; the `imile-win` host uses profile `local`.
