# Backup Vault on Kubernetes

A Kustomize base for the backup service, an example overlay to start yours
from, and a Component that installs the web extension into OpenCloud's own
Deployment.

```
base/                       the service, hardened; use it through an overlay
  backupd.env               the settings that are not secrets, documented
  operator-jobs.yaml        the operator commands, as suspended CronJobs
overlays/example/           every value only your cluster knows, as REPLACE_ME
components/web-extension/   the web extension, for OpenCloud's Deployment
```

Validated against **Kubernetes 1.35 to 1.37** (kubeconform, oldest and
newest). The NetworkPolicy and the web-extension Component are tested on a
kind cluster with Calico (`make k8s-kind-test`).

Use the manifests from a release archive: they name that release's images by
digest. The copy in the repository names none (`unreleased`, which no
registry has), so its pods do not start until your overlay names a version in
`images:`.

## What the base does

- One replica, `Recreate`: two instances against one state Space are refused
  at startup, and a rolling update would start the second.
- Its own ServiceAccount, with no role and no token mounted.
- Non-root, read-only root filesystem, no capabilities, `RuntimeDefault`
  seccomp; the work directory and `/tmp` in memory.
- Startup, liveness and readiness probes. `/readyz` answers 200 once
  OpenCloud's gateway is reachable.
- A NetworkPolicy: in, only from your ingress controller's namespace on
  8080; out, only DNS, OpenCloud's namespace on 9142 and 9158, and 443 and
  3900 anywhere (OpenCloud's public URL, the backup destinations). Narrowing
  443 and 3900 to your ingress and your destinations is yours to do. Whether
  OpenCloud admits only this service on 9142 and 9158 is OpenCloud's own
  policy to set.
- `app.kubernetes.io/{name,component,part-of,version}` labels.
- Go's memory limit is not set here: the service derives it from the
  container's limit, less the memory-backed volumes (`GOMEMLIMIT` overrides
  it).

## Installing

OpenCloud first: expose its gateway and data server on a Service, and allow
WebAssembly in its Content Security Policy (README.md, "Deployment: step by
step", steps 1 and 2).

1. **Copy `overlays/example`** into your own repository, and point its
   `resources:` at this base (a path, or a remote base at a release tag).
   Set `namespace:`.
2. **Secrets.** Create the Secret imperatively, which keeps the keys out of
   any file, and drop `secret-wrap-keys.yaml` from the overlay's resources:

   ```sh
   kubectl -n opencloud-backup create secret generic opencloud-backup-wrap-keys \
     --from-literal=srw-wrapping-key="$(openssl rand -base64 32)" \
     --from-literal=tw-wrapping-key="$(openssl rand -base64 32)" \
     --from-literal=service-account-id="$OC_SERVICE_ACCOUNT_ID" \
     --from-literal=service-account-secret="$OC_SERVICE_ACCOUNT_SECRET"
   ```

   The service account is OpenCloud's (`service_account_id` and
   `service_account_secret` in its `opencloud.yaml`). Keep a copy of the two
   keys outside the cluster.
3. **Fill in the overlay**: `backupd.env` (all but `STATE_SPACE_ID`), and the
   Ingress and NetworkPolicy values in `kustomization.yaml`. The Ingress
   serves OpenCloud's own host, path `/backup/api`.
4. **Apply** with `kubectl apply -k <your overlay>`. The service does not
   start yet: it refuses the placeholder in `STATE_SPACE_ID`.
5. **Create the state Space**, once:

   ```sh
   kubectl -n opencloud-backup create job --from=cronjob/opencloud-backup-provision-state-space provision
   kubectl -n opencloud-backup logs -f job/provision
   ```

   The last line is the id. Put it into `STATE_SPACE_ID`, apply again, and
   delete the job. Never run it a second time: a second Space would start
   empty, and the first holds every wrapped key.
6. **The web extension**, below, then add a backup destination as an
   OpenCloud admin in Backup Vault's admin view, set up one Space, back it up
   and restore from it before relying on it.

## Operator commands

The CronJobs in `operator-jobs.yaml` never run by themselves. Run one with
`kubectl create job --from=cronjob/<name> <job-name>`, read it with
`kubectl logs`, and delete the job afterwards.

- **`opencloud-backup-rotate-srw` / `-rotate-tw`** retire a wrapping key
  (README.md, "Recovery runbook"). Put the new key into `srw-wrapping-key`
  (or `tw-`) and the retiring one into `srw-wrapping-key-old`, scale the
  Deployment to 0, run the job, scale back to 1, and remove the old key.
  Re-running finishes an interrupted rotation.
- **`opencloud-backup-takeout`** copies one Space's backups out of the
  bucket, still encrypted, for its owner to open with their Recovery Key.
  Create the Secret `opencloud-backup-takeout` (`access-key-id`,
  `secret-access-key`) and the volume claim `opencloud-backup-takeout`, and
  set its flags in your overlay.

## The web extension

The extension runs inside OpenCloud's web UI, so it is installed into
OpenCloud's pod, not this base's. OpenCloud reads its apps directory at
startup only.

### With Kustomize

Add `components/web-extension` to the kustomization that deploys OpenCloud.
An initContainer copies the extension into
`<data volume>/web/assets/apps/backup-vault` as OpenCloud's user. The patch
assumes a Deployment named `opencloud` with its data volume `data` at
`/var/lib/opencloud`; change those in `patch.yaml` to yours.

### With Helm

OpenCloud's community Helm charts
([opencloud-eu/helm](https://github.com/opencloud-eu/helm)) are archived, as
of this writing; the charts OpenCloud supports come with its business
subscription. What follows is read from the community charts' last commit
(72de94b) and is not tested here.

- **`opencloud-microservices`**: the web service takes extra
  initContainers, and its apps directory is the volume `apps`. The image's
  default copies into `/extensions/backup-vault`:

  ```yaml
  services:
    web:
      additionalInitContainers:
      - name: backup-vault-web
        image: ghcr.io/knoppiks/opencloud-backup-vault-web:<version>
        volumeMounts:
        - name: apps
          mountPath: /extensions
  ```

- **`opencloud`** has no place for another initContainer. Add the
  Component with a Helm post-renderer that runs Kustomize over the
  rendered chart.

Never set `command` on the extension's container: the image has no shell,
and its entrypoint does the copy.
