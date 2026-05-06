# API Reference

## Packages
- [projection.sh/v1](#projectionshv1)


## projection.sh/v1

Package v1 contains API Schema definitions for the projection v1 API group

### Resource Types
- [ClusterProjection](#clusterprojection)
- [ClusterProjectionList](#clusterprojectionlist)
- [Projection](#projection)
- [ProjectionList](#projectionlist)



#### ClusterProjection



ClusterProjection mirrors one source object into many destination namespaces.



_Appears in:_
- [ClusterProjectionList](#clusterprojectionlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.sh/v1` | | |
| `kind` _string_ | `ClusterProjection` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ClusterProjectionSpec](#clusterprojectionspec)_ |  |  |  |
| `status` _[ClusterProjectionStatus](#clusterprojectionstatus)_ |  |  |  |


#### ClusterProjectionDestination



ClusterProjectionDestination is the cluster-scoped destination spec.

`namespaces` and `namespaceSelector` are mutually exclusive (CEL admission)
and at least one must be set.



_Appears in:_
- [ClusterProjectionSpec](#clusterprojectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespaces` _string array_ | Namespaces is an explicit list of destination namespaces. Mutually<br />exclusive with NamespaceSelector. Each entry is a DNS-1123 label. |  | Optional: \{\} <br /> |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#labelselector-v1-meta)_ | NamespaceSelector picks destination namespaces by label. Mutually<br />exclusive with Namespaces. |  | Optional: \{\} <br /> |
| `name` _string_ | Name in each destination namespace. Defaults to Source.Name when empty.<br />The same Name is written into every targeted namespace. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |


#### ClusterProjectionList



ClusterProjectionList contains a list of ClusterProjection.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.sh/v1` | | |
| `kind` _string_ | `ClusterProjectionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[ClusterProjection](#clusterprojection) array_ |  |  |  |


#### ClusterProjectionSpec



ClusterProjectionSpec is the desired state of a cluster-scoped Projection.

A ClusterProjection fans out one source object into many namespaces
selected by either an explicit list or a label selector. Cluster-tier
authority is required to create one — the chart does NOT aggregate the
`clusterprojections` CRUD verbs into the built-in `admin`/`edit` roles.



_Appears in:_
- [ClusterProjection](#clusterprojection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _[SourceRef](#sourceref)_ | Source identifies the object to project. |  |  |
| `destination` _[ClusterProjectionDestination](#clusterprojectiondestination)_ | Destination configures the fan-out target set and optional rename. |  |  |
| `overlay` _[Overlay](#overlay)_ | Overlay applies metadata patches uniformly to every projected copy.<br />(Per-target overlays are not supported in v0.3.) |  | Optional: \{\} <br /> |


#### ClusterProjectionStatus



ClusterProjectionStatus reports the rollup of the most recent reconcile.



_Appears in:_
- [ClusterProjection](#clusterprojection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#condition-v1-meta) array_ | Conditions: SourceResolved, DestinationWritten, Ready. |  | Optional: \{\} <br /> |
| `destinationName` _string_ | DestinationName is the resolved name of the destination object,<br />identical across all targeted namespaces. Populated after the first<br />successful write; empty before that. |  | Optional: \{\} <br /> |
| `namespacesWritten` _integer_ | NamespacesWritten is the count of namespaces in which the destination<br />was successfully created or updated during the most recent reconcile. |  | Optional: \{\} <br /> |
| `namespacesFailed` _integer_ | NamespacesFailed is the count of namespaces where the write failed<br />during the most recent reconcile. The DestinationWritten condition's<br />message carries a (truncated) list of failed namespace names. |  | Optional: \{\} <br /> |


#### Overlay



Overlay applies metadata patches to the projected object on top of what the
source carries. Only labels and annotations are mergeable — name and
namespace cannot be touched (they are set by the controller from the
destination spec).



_Appears in:_
- [ClusterProjectionSpec](#clusterprojectionspec)
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ | Labels merged onto the destination's metadata.labels. Source labels<br />win on conflict for keys the source already has; overlay wins for<br />overlay-only keys. (See concepts.md for the full merge rule.) |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations merged onto the destination's metadata.annotations.<br />Same merge rule as Labels. |  | Optional: \{\} <br /> |


#### Projection



Projection mirrors one source object into the Projection's own namespace.



_Appears in:_
- [ProjectionList](#projectionlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.sh/v1` | | |
| `kind` _string_ | `Projection` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ProjectionSpec](#projectionspec)_ |  |  |  |
| `status` _[ProjectionStatus](#projectionstatus)_ |  |  |  |


#### ProjectionDestination



ProjectionDestination configures the rename override for a namespaced Projection.



_Appears in:_
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name in the destination namespace. Defaults to Source.Name when empty. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br />Optional: \{\} <br /> |


#### ProjectionList



ProjectionList contains a list of Projection.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.sh/v1` | | |
| `kind` _string_ | `ProjectionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[Projection](#projection) array_ |  |  |  |


#### ProjectionSpec



ProjectionSpec is the desired state of a namespaced Projection.

A Projection mirrors one source object into the Projection's own namespace.
It cannot write outside its own namespace — that is what ClusterProjection
is for. This narrowness is deliberate: it makes namespace-scoped RBAC on
`projections.projection.sh` a structural confinement, not a policy hint.



_Appears in:_
- [Projection](#projection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _[SourceRef](#sourceref)_ | Source identifies the object to project. |  |  |
| `destination` _[ProjectionDestination](#projectiondestination)_ | Destination optionally renames the destination. The destination<br />namespace is implicitly the Projection's own namespace and cannot<br />be set; for cross-namespace mirroring use ClusterProjection. |  | Optional: \{\} <br /> |
| `overlay` _[Overlay](#overlay)_ | Overlay applies metadata patches on top of the projected object. |  | Optional: \{\} <br /> |


#### ProjectionStatus



ProjectionStatus reports the most recent reconcile outcome.



_Appears in:_
- [Projection](#projection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#condition-v1-meta) array_ | Conditions: SourceResolved, DestinationWritten, Ready. |  | Optional: \{\} <br /> |
| `destinationName` _string_ | DestinationName is the resolved name of the destination object after<br />applying any rename override (`spec.destination.name`) or defaulting to<br />`spec.source.name`. Populated by the controller after a successful<br />write; empty before the first reconcile completes. |  | Optional: \{\} <br /> |


#### SourceRef



SourceRef identifies the object to project.

Group + Version + Kind name the GVK; Namespace + Name name the object.

`version` may be omitted for non-core groups, in which case the operator
resolves the preferred served version via the RESTMapper on every
reconcile. The core group has only `v1` as a stable form, so `version`
MUST be set when `group` is empty — enforced by the CEL rule below.



_Appears in:_
- [ClusterProjectionSpec](#clusterprojectionspec)
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `group` _string_ | Group is the API group of the source object. Empty string means the<br />core group (e.g. ConfigMap, Secret, Service). |  | Pattern: `^$\|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Optional: \{\} <br /> |
| `version` _string_ | Version is the API version of the source object within its Group. Omit<br />for non-core groups to use the RESTMapper's preferred served version<br />(the source automatically follows CRD version promotions). |  | Pattern: `^$\|^v[0-9]+([a-z]+[0-9]+)?$` <br />Optional: \{\} <br /> |
| `kind` _string_ | Kind is the API Kind of the source object (PascalCase). |  | Pattern: `^[A-Z][a-zA-Z0-9]*$` <br /> |
| `namespace` _string_ | Namespace where the source object lives. |  | MaxLength: 63 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br /> |
| `name` _string_ | Name of the source object. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` <br /> |


