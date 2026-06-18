# Kubernetes deployment (P4-4)

Production-shaped manifests to run the conductor daemon
([`Dockerfile`](../../Dockerfile), P4-1) with its health endpoints
([`cmd/conductor/httpserver.go`](../../cmd/conductor/httpserver.go), P4-2) on a
cluster. This is a [Kustomize](https://kustomize.io/) base.

> **No real secret is committed.** `secret.yaml` and the Postgres password in
> `postgres.yaml` are **obvious placeholders** (`CHANGEME` / `REPLACE_ME`). Use a
> real secret manager for production (see [Set the real Secret](#1-set-the-real-secret)).

## Files

| File | What it is |
|------|------------|
| `namespace.yaml` | the `conductor-platform` namespace |
| `configmap.yaml` | non-secret env (project, root, http-addr, interval, base branch, governance, governor caps, heartbeat path) |
| `secret.yaml` | **TEMPLATE** Secret: `CONDUCTOR_DSN` + `GH_TOKEN` placeholders |
| `postgres.yaml` | **dev/bootstrap** Postgres (StatefulSet + headless Service + PVC + dev-password Secret) |
| `deployment.yaml` | the conductor Deployment (1 replica, probes, hardened securityContext, `/workspace` emptyDir) |
| `service.yaml` | ClusterIP Service exposing `:8080` (`http`) |
| `cronjob-stallcheck.yaml` | the independent stall detector (ADR-0016) — curls `/readyz` over the Service every minute |
| `kustomization.yaml` | ties it all together |

## 1. Set the real Secret

The committed `secret.yaml` is a template with **placeholders only**. Do **not**
commit real values. In production manage the Secret with a real secret manager:

- **Sealed Secrets**, **SOPS** (age/KMS), or **External Secrets Operator**
  (Vault / AWS SM / GCP SM / Infisical).

For a quick non-production test you can override it imperatively instead of
editing the file:

```sh
kubectl -n conductor-platform create secret generic conductor-secret \
  --from-literal=CONDUCTOR_DSN='postgres://conductor:REALPASS@conductor-postgres:5432/conductor?sslmode=disable' \
  --from-literal=GH_TOKEN='ghp_xxx' \
  --dry-run=client -o yaml | kubectl apply -f -
```

The DSN's password **must match** the Postgres password (`postgres.yaml`'s
`conductor-postgres-secret`). For production, drop `postgres.yaml` and point the
DSN at **managed Postgres** with `sslmode=require`.

## 2. Push the image

The Deployment uses `conductor-platform:latest`. Build it and push it to a
registry the cluster can pull (the manifest's `imagePullPolicy: IfNotPresent`
expects the tag to be resolvable):

```sh
make docker-build                                   # builds conductor-platform:latest
docker tag conductor-platform:latest <registry>/conductor-platform:<tag>
docker push <registry>/conductor-platform:<tag>
# then set the image (overlay or kustomize edit):
#   cd deploy/k8s && kustomize edit set image conductor-platform=<registry>/conductor-platform:<tag>
```

## 3. Apply

```sh
kustomize build deploy/k8s/ | kubectl apply --dry-run=client -f -   # validate (no cluster needed)
kustomize build deploy/k8s/ | kubectl apply -f -                    # apply for real
kubectl -n conductor-platform get pods,svc,cronjob
```

## What the probes do

Wired to the daemon's health server (enabled via `CONDUCTOR_HTTP_ADDR=:8080`):

| Probe | Endpoint | Behavior |
|-------|----------|----------|
| `livenessProbe` | `GET /healthz` | 200 while the tick loop runs; **does not touch the DB**, so a Postgres blip never flaps liveness (k8s won't kill a healthy daemon) |
| `readinessProbe` | `GET /readyz` | 200 when the store is reachable (cheap `ListProjects`); 503 otherwise — allowed to flap with DB health |
| (ops) | `GET /status` | last-tick snapshot + backend name (`memory`/`postgres`); **never** the DSN |

## securityContext hardening

`runAsNonRoot: true`, `runAsUser/runAsGroup/fsGroup: 65532` (the image's non-root
uid), `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`,
`seccompProfile: RuntimeDefault`. The rootfs is left **writable**
(`readOnlyRootFilesystem: false`) because the provisioner shells out to
`git clone`/`git worktree`, which writes config + temp files in scattered places;
all heavy writes are confined to the `/workspace` emptyDir volume.

## Independent stall detector (ADR-0016)

`cronjob-stallcheck.yaml` is the **out-of-process backstop** ADR-0016 mandates —
the daemon cannot recover its own death, so a separate process must observe it.
The native `conductor -check -heartbeat <file>` mode reads a heartbeat **file**,
but that file lives on the daemon pod's emptyDir and is **not shared across
pods** (sharing it would need fragile RWX storage). The clean k8s-native
equivalent: this CronJob **curls `/readyz` over the `conductor` Service every
minute**. `/readyz` is 200 only when the loop is up and the store is reachable, so
a non-200/unreachable result is the same "not making progress" signal — but it is
genuinely independent (a different pod scheduled by k8s, surviving the daemon's
death). On failure the Job exits non-zero, which alerting fires on; k8s'
`livenessProbe` does the actual restart.

## Scaling caveat

Keep `replicas: 1`. The Postgres lease table + governor (ADR-0008/0010) enforce
single-active work per repo, so extra replicas would just idle. The Deployment
uses `strategy: Recreate` so a rollout never briefly double-runs the daemon.
