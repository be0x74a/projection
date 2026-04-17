# projection

A Helm chart for the [projection](https://github.com/be0x74a/projection)
Kubernetes operator. Projection is a CRD for declarative mirroring of Kubernetes
resources across namespaces.

## What this chart installs

- The `Projection` CustomResourceDefinition (first install only; see CRD
  lifecycle note below).
- A cluster-scoped controller Deployment running the `projection` manager.
- A ServiceAccount plus a ClusterRole / ClusterRoleBinding granting the
  controller the privileges it needs to mirror any resource Kind.
- A namespaced Role / RoleBinding for leader election (leases + events) in the
  release namespace.
- A ClusterIP Service exposing Prometheus metrics on port 8443 (HTTPS,
  secure-by-default with the controller-runtime authn/authz filter).
- A `metrics-reader` ClusterRole you can bind to a scrape identity.

**Optional production-grade resources** (all opt-in): a ServiceMonitor for prometheus-operator scrape wiring (`serviceMonitor.enabled`), a NetworkPolicy locking controller egress to the Kubernetes API and cluster DNS (`networkPolicy.enabled`), and a PodDisruptionBudget keeping the controller available through voluntary disruptions (`podDisruptionBudget.enabled`).

## Prerequisites

- Kubernetes >= 1.25
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
  --set image.tag=v0.1.0
```

## Upgrade

```shell
helm upgrade projection charts/projection --namespace projection-system
```

Note: Helm will NOT update CRDs on upgrade. If the CRD schema has changed
between chart versions you must apply the new CRD manually:

```shell
kubectl apply -f charts/projection/crds/projections.projection.be0x74a.io.yaml
```

## Uninstall

```shell
helm uninstall projection --namespace projection-system
```

The CRD is intentionally left behind to protect any existing `Projection`
custom resources. To remove it:

```shell
kubectl delete crd projections.projection.be0x74a.io
```

## CRD lifecycle

Helm 3 installs files under `crds/` only on the FIRST install of a release.
Upgrades and uninstalls do NOT touch the CRD. Manage the CRD manually (via
`kubectl apply`) when you roll out schema changes or share a single CRD across
multiple releases.

## Values

| Key                                 | Default                       | Description                                                                 |
| ----------------------------------- | ----------------------------- | --------------------------------------------------------------------------- |
| `image.repository`                  | `ghcr.io/be0x74a/projection`  | Controller image repository.                                                |
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
| `crds.install`                      | `true`                        | Documentation flag — Helm always installs `crds/` on first install.         |
| `serviceMonitor.enabled`            | `false`                       | Render a ServiceMonitor selecting the metrics Service. Requires `monitoring.coreos.com/v1`. |
| `serviceMonitor.interval`           | `30s`                         | Scrape interval for the ServiceMonitor.                                     |
| `serviceMonitor.scrapeTimeout`      | `10s`                         | Scrape timeout for the ServiceMonitor.                                      |
| `serviceMonitor.labels`             | `{}`                          | Extra labels for prometheus-operator's `serviceMonitorSelector`.            |
| `serviceMonitor.tlsConfig`          | `insecureSkipVerify: true`    | TLS config for scraping the HTTPS metrics endpoint.                         |
| `networkPolicy.enabled`             | `false`                       | Render a NetworkPolicy restricting controller egress.                       |
| `networkPolicy.dns`                 | `kube-system / k8s-app=kube-dns / 53` | Cluster DNS pod selector for the DNS egress rule.                     |
| `networkPolicy.extraEgress`         | `[]`                          | Extra egress rules (each a NetworkPolicyEgressRule).                        |
| `podDisruptionBudget.enabled`       | `false`                       | Render a PodDisruptionBudget for the controller Deployment.                 |
| `podDisruptionBudget.minAvailable`  | `1`                           | Minimum pods available. Set exactly one of minAvailable / maxUnavailable.   |
| `podDisruptionBudget.maxUnavailable`| `null`                        | Max pods unavailable. Leave null when using minAvailable.                   |

## Example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: source-cm
  namespace: default
data:
  greeting: hello
---
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: mirror-greeting
  namespace: default
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: source-cm
    namespace: default
  destination:
    namespace: team-a
    name: greeting
```
