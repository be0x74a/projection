# projection

A Helm chart for the [projection](https://github.com/projection-operator/projection)
Kubernetes operator. Projection ships two CustomResourceDefinitions for declarative
mirroring of Kubernetes resources:

- **`Projection`** (namespaced, single-target) — mirrors one source object into the
  Projection's own `metadata.namespace`. Cannot write outside its own namespace, so
  namespace-scoped RBAC on `projections.projection.sh` is a structural confinement,
  not a policy hint.
- **`ClusterProjection`** (cluster-scoped, fan-out) — mirrors one source object into
  many destination namespaces, selected by an explicit list (`spec.destination.namespaces`)
  or a label selector (`spec.destination.namespaceSelector`). Creating one requires
  cluster-tier authority (see `rbac.aggregate` below).

## What this chart installs

- The `Projection` and `ClusterProjection` CustomResourceDefinitions (first install
  only; see CRD lifecycle note below).
- A cluster-scoped controller Deployment running the `projection` manager.
- A ServiceAccount plus a ClusterRole / ClusterRoleBinding granting the
  controller the privileges it needs to mirror resource Kinds. The set of
  Kinds the controller may touch is governed by the `supportedKinds` value
  (defaults to `*/*` for backwards-compatibility; narrow it for regulated
  clusters — see [docs/security.md](../../docs/security.md)).
- **End-user-facing RBAC** (`rbac.aggregate=true` by default):
  - `<release>-projection-namespaced-edit` ClusterRole — aggregates into the
    cluster-default `admin` and `edit` roles, granting namespace tenants
    `create`/`update`/`delete`/`patch` on `projections.projection.sh` in their
    own namespaces. Does NOT grant any access to `clusterprojections.projection.sh`.
  - `<release>-projection-namespaced-view` ClusterRole — aggregates into the
    cluster-default `view` role, granting namespace tenants read-only access
    to `projections.projection.sh` only.
  - `<release>-projection-cluster-admin` ClusterRole — full CRUD on
    `clusterprojections.projection.sh`. NOT aggregated; bind explicitly via
    ClusterRoleBinding to operators who manage cluster-scoped fan-out.
    Always rendered regardless of the `rbac.aggregate` toggle. Namespace
    tenants are deliberately isolated from the cluster tier — viewing or
    editing `ClusterProjection` requires this binding.
- A namespaced Role / RoleBinding for leader election (leases + events) in the
  release namespace.
- A ClusterIP Service exposing Prometheus metrics on port 8443 (HTTPS,
  secure-by-default with the controller-runtime authn/authz filter).
- A `metrics-reader` ClusterRole you can bind to a scrape identity.

**Optional production-grade resources** (all opt-in): a ServiceMonitor for prometheus-operator scrape wiring (`serviceMonitor.enabled`), a NetworkPolicy locking controller egress to the Kubernetes API and cluster DNS (`networkPolicy.enabled`), and a PodDisruptionBudget keeping the controller available through voluntary disruptions (`podDisruptionBudget.enabled`).

## Prerequisites

- Kubernetes >= 1.32
- Helm >= 3.8
- Cluster-admin permissions for the user running `helm install` (required to
  create the ClusterRole and install the CRD)

## Install

```shell
helm install projection charts/projection \
  --namespace projection-system --create-namespace
```

Override the image for local / air-gapped deployments:

```shell
helm install projection charts/projection \
  --namespace projection-system --create-namespace \
  --set image.repository=my-registry/projection \
  --set image.tag=v0.3.0
```

## Upgrade

```shell
helm upgrade projection charts/projection --namespace projection-system
```

Note: Helm will NOT update CRDs on upgrade. If the CRD schema has changed
between chart versions you must apply the new CRDs manually:

```shell
kubectl apply -f charts/projection/crds/projections.projection.sh.yaml
kubectl apply -f charts/projection/crds/clusterprojections.projection.sh.yaml
```

## Uninstall

```shell
helm uninstall projection --namespace projection-system
```

The CRDs are intentionally left behind to protect any existing `Projection`
or `ClusterProjection` custom resources. To remove them:

```shell
kubectl delete crd projections.projection.sh clusterprojections.projection.sh
```

## CRD lifecycle

Helm 3 installs files under `crds/` only on the FIRST install of a release.
Upgrades and uninstalls do NOT touch the CRD. Manage the CRD manually (via
`kubectl apply`) when you roll out schema changes or share a single CRD across
multiple releases.

## Values

| Key                                 | Default                       | Description                                                                 |
| ----------------------------------- | ----------------------------- | --------------------------------------------------------------------------- |
| `image.repository`                  | `ghcr.io/projection-operator/projection`  | Controller image repository.                                                |
| `image.tag`                         | `""` (falls back to AppVersion) | Controller image tag.                                                     |
| `image.pullPolicy`                  | `IfNotPresent`                | Controller image pull policy.                                               |
| `imagePullSecrets`                  | `[]`                          | imagePullSecrets referenced by the pod.                                    |
| `nameOverride`                      | `""`                          | Override the chart-name portion of resource names.                         |
| `fullnameOverride`                  | `""`                          | Override the full resource-name template.                                  |
| `replicaCount`                      | `1`                           | Controller replicas. Values > 1 require leaderElection.enabled=true.        |
| `leaderElection.enabled`            | `true`                        | Enable leader election in the release namespace.                           |
| `metrics.enabled`                   | `true`                        | Expose the metrics endpoint and Service.                                    |
| `metrics.secure`                    | `true`                        | Serve metrics over HTTPS with authn/authz filter.                          |
| `metrics.bindAddress`               | `:8443`                       | Metrics bind address (controller arg).                                      |
| `metrics.service.type`              | `ClusterIP`                   | Type for the metrics Service.                                              |
| `metrics.service.port`              | `8443`                        | Port for the metrics Service.                                              |
| `healthProbe.bindAddress`           | `:8081`                       | Health probe bind address.                                                  |
| `resources`                         | see values.yaml               | Controller container resource requests/limits.                              |
| `nodeSelector`                      | `{}`                          | Pod nodeSelector.                                                          |
| `tolerations`                       | `[]`                          | Pod tolerations.                                                            |
| `affinity`                          | `{}`                          | Pod affinity rules.                                                        |
| `topologySpreadConstraints`         | `[]`                          | Pod topology spread constraints.                                           |
| `securityContext.pod`               | restricted profile            | Pod-level securityContext (runAsNonRoot, fsGroup, seccompProfile).         |
| `securityContext.container`         | restricted profile            | Container-level securityContext (drop ALL caps, read-only root FS).        |
| `serviceAccount.create`             | `true`                        | Create a dedicated ServiceAccount.                                          |
| `serviceAccount.name`               | `""`                          | Override generated ServiceAccount name.                                     |
| `serviceAccount.annotations`        | `{}`                          | Annotations for the ServiceAccount (e.g. IRSA).                            |
| `rbac.aggregate`                    | `true`                        | Render the `<release>-projection-namespaced-edit` and `<release>-projection-namespaced-view` ClusterRoles with aggregation labels so the cluster-default `admin`/`edit`/`view` roles automatically gain `Projection`/`ClusterProjection` access for namespace tenants. The `<release>-projection-cluster-admin` ClusterRole (for `ClusterProjection` CRUD) is rendered regardless of this flag and must be bound explicitly. |
| `crds.install`                      | `true`                        | Documentation flag — Helm always installs `crds/` on first install.         |
| `serviceMonitor.enabled`            | `false`                       | Render a ServiceMonitor selecting the metrics Service. Requires `monitoring.coreos.com/v1`. |
| `serviceMonitor.interval`           | `30s`                         | Scrape interval for the ServiceMonitor.                                     |
| `serviceMonitor.scrapeTimeout`      | `10s`                         | Scrape timeout for the ServiceMonitor.                                      |
| `serviceMonitor.labels`             | `{}`                          | Extra labels for prometheus-operator's `serviceMonitorSelector`.            |
| `serviceMonitor.tlsConfig`          | `insecureSkipVerify: true`    | TLS config for scraping the HTTPS metrics endpoint.                         |
| `networkPolicy.enabled`             | `false`                       | Render a NetworkPolicy restricting controller egress.                       |
| `networkPolicy.dns`                 | object: `namespace`, `podLabels`, `port` (defaults to `kube-system` / `k8s-app: kube-dns` / `53`) | Cluster DNS pod selector for the DNS egress rule.                     |
| `networkPolicy.extraEgress`         | `[]`                          | Extra egress rules (each a NetworkPolicyEgressRule).                        |
| `podDisruptionBudget.enabled`       | `false`                       | Render a PodDisruptionBudget for the controller Deployment.                 |
| `podDisruptionBudget.minAvailable`  | `1`                           | Minimum pods available. Set exactly one of minAvailable / maxUnavailable.   |
| `podDisruptionBudget.maxUnavailable`| `null`                        | Max pods unavailable. Leave null when using minAvailable.                   |
| `requeueInterval`                   | `30s`                         | Requeue cadence for reconciliation. See observability.md for tuning guidance. |
| `leaderElection.leaseDuration`      | `15s`                         | Leader-election lease duration. Only effective when `leaderElection.enabled=true`. |
| `selectorWriteConcurrency`          | `16`                          | Per-ClusterProjection in-flight destination-write cap during selector fan-out. Has no effect on namespaced `Projection` (single-target only). Must be > 0. Raise for selectors matching thousands of namespaces; lower on apiserver-constrained clusters. See [docs/observability.md](../../docs/observability.md) for the rationale. |
| `sourceMode`                        | `allowlist`                   | Source projectability policy. `allowlist` requires source objects to carry `projection.sh/projectable="true"`; `permissive` allows any source unless explicitly opted out. See [docs/concepts.md](../../docs/concepts.md). |
| `supportedKinds`                    | `[{apiGroup: "*", resources: ["*"]}]` | RBAC scope for the controller's ClusterRole. Default preserves pre-v0.2 cluster-admin-equivalent access. Replace with an explicit list to narrow; `[]` grants nothing beyond Projection CRs. See [docs/security.md](../../docs/security.md). |

## Values validation

The chart ships a `values.schema.json` that Helm consults during `install`, `upgrade`, `lint`, and `template`. Malformed overrides — typos at the top level (e.g. `replicaCounts` instead of `replicaCount`), wrong types, or `supportedKinds` entries missing the required `apiGroup`/`resources` fields — fail with a clear pre-install error instead of silently using defaults.

Editors with JSON-schema support also pick this up. For VS Code with `redhat.vscode-yaml`, the schema can be associated explicitly via a [modeline](https://github.com/redhat-developer/vscode-yaml#using-inlined-schema) at the top of your overrides file:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/projection-operator/projection/main/charts/projection/values.schema.json
```

Schema strictness is pragmatic: top-level and chart-defined nested keys are locked down (`additionalProperties: false`) so typos surface early; pass-through Kubernetes shapes (`nodeSelector`, `tolerations`, `affinity`, `securityContext.{pod,container}` contents, `networkPolicy.extraEgress[]`, `serviceMonitor.tlsConfig`) stay opaque — the API server validates those authoritatively.

## Examples

### Namespaced single-target mirror (`Projection`)

A `Projection` always writes into its own `metadata.namespace`. The example below
mirrors `default/source-cm` into `team-a/greeting` by placing the Projection in
`team-a`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: source-cm
  namespace: default
  annotations:
    # Required when the controller runs with the default sourceMode=allowlist.
    # Skip this annotation if you set sourceMode=permissive.
    projection.sh/projectable: "true"
data:
  greeting: hello
---
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: mirror-greeting
  namespace: team-a
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: source-cm
    namespace: default
  destination:
    name: greeting
```

### Cluster-scoped fan-out (`ClusterProjection`)

A `ClusterProjection` mirrors one source into many namespaces, picked by label
selector or by an explicit `namespaces` list. The two are mutually exclusive
(CEL admission); set exactly one.

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: mirror-greeting-fanout
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: source-cm
    namespace: default
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror: "true"
  overlay:
    labels:
      projected-by: projection
```

More example shapes — including explicit-list fan-out, overlays, and
non-ConfigMap Kinds — live under [`examples/`](../../examples/).
