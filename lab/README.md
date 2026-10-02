# Portainer 2.39.1 ECR nested-writer regression

Base: `2c1f3c27d5b817ba405f44efe225972e789c2ec1`. This branch is a laboratory candidate, not a production release.

The stack PUT opens a writable bbolt transaction around synchronous deployment. Compose previously refreshed expired ECR tokens via the global datastore, opening a second writer. The first writer waits for deployment; deployment waits for the second writer. The fix passes a registry update callback bound to the active transaction through Compose deployment options (including Pull and Up). Nontransactional callers keep normal datastore persistence. No global transaction state or database schema changes.

ECR now has a 30-second deadline covering the complete SDK call and retries. A context-aware entry point supports earlier cancellation. This bounds AWS waits separately from the transaction repair. Existing best-effort registry-error handling is retained: an AWS error may still allow a local/no-pull Compose deployment to succeed. This patch does not make a network deployment and filesystem writes atomic with BoltDB. In particular, the existing error handler restores Compose but does not restore stack.env; rollback tests explicitly verify database and Compose behavior rather than claiming complete filesystem rollback.

## Reproduction and tests

Prerequisites: Go 1.25.8, Docker for integration only. The transaction fixture uses real bbolt, the HTTP stack handler, real deployment wrappers and AWS SDK with a loopback TLS ECR stub. No real credentials or AWS calls. Its Compose plugin is a stub except in the optional isolated Docker test. Keep the TLS fixture in a fresh test process: Go caches certificate roots and proxy environment.

```sh
# Baseline eaf8500ce09b609f677517f2c725acde6fda3b21: expected timeout with
# beginRWTx -> registry Update -> stackUpdate's outer Update in one goroutine.
go test ./api/http/handler/stacks -run '^TestStackUpdateECRTransaction$' -count=1 -timeout=20s -v

# Fixed branch: expired, valid, non-ECR, AWS error, Compose rollback,
# force-pull and concurrent two-stack updates.
go test ./api/http/handler/stacks ./api/aws/ecr -count=1 -timeout=120s -v

# Dedicated disposable Docker daemon ONLY. Preload alpine:3.20.
ASTRA_PORTAINER_E2E=1 DOCKER_HOST=tcp://isolated-dind:2375 go test ./api/http/handler/stacks -run '^TestStackUpdateECRTransaction/expired$' -count=1 -timeout=60s -v
```

The Docker test creates `ecr-lab-web-1` in the disposable daemon. Destroy only the dedicated laboratory after evidence collection; never point it at production or a shared Docker socket.

Safe debug events record registry/stack IDs, duration and success for ECR refresh, registry persistence, deployment and transaction return. A persistence log inside a transaction is not a commit receipt. A transaction failure can include a commit error; the failure log does not certify external-effect rollback.

## Build

Build the server from the exact patch commit, then use `lab/Dockerfile`. It retains frontend and ancillary files from the digest-pinned official 2.39.1 image. Match binary architecture with the selected base manifest; do not put an ARM64 binary into an AMD64 image. The lab tag is `portainer:2.39.1-bayai-fix`.

## Promotion / rollback

Production replacement and stack PUT require separate explicit authorization. Establish global exclusivity, record the old immutable image and container configuration, take and verify a consistent complete /data backup, replace only Portainer preserving /data, and verify API/UI and stack reads before any stack PUT. Then authorize one PUT separately and validate terminal HTTP, metadata, files and unchanged application identities. A timeout is uncertain: reconcile before any retry. Roll back only with the replacement process stopped and the database quiescent, using the saved original image and matched /data snapshot as necessary. Do not restore a database under an active writer or claim image rollback undoes application effects.
