# API Reference

## Packages
- [projection.be0x74a.io/v1](#projectionbe0x74aiov1)


## projection.be0x74a.io/v1

Package v1 contains API Schema definitions for the projection v1 API group

### Resource Types
- [Projection](#projection)
- [ProjectionList](#projectionlist)



#### DestinationRef



DestinationRef identifies where the projected object should be written.
Invariant: namespace and namespaceSelector are mutually exclusive. Enforced
at admission time by the CEL rule below (requires k8s 1.32+) and, as
defense-in-depth, also by the reconciler.



_Appears in:_
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespace` _string_ | Namespace to project into. Defaults to the Projection's own namespace.<br />Mutually exclusive with NamespaceSelector. |  | MaxLength: 63 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#labelselector-v1-meta)_ | NamespaceSelector selects namespaces to project into by label.<br />Mutually exclusive with Namespace. |  | Optional: \{\} <br /> |
| `name` _string_ | Name in the destination namespace. Defaults to Source.Name. DNS-1123<br />subdomain: lowercase alphanumerics, '-', and '.', up to 253 chars. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Optional: \{\} <br /> |


#### Overlay



Overlay is applied on top of the source object's metadata before writing
to the destination. Overlay entries win on key conflicts with the source.



_Appears in:_
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ | Labels are merged with the source object's metadata.labels before<br />writing to the destination. Keys set here win on conflict with<br />source labels. |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations are merged with the source object's metadata.annotations<br />before writing to the destination. Keys set here win on conflict<br />with source annotations. Note: the controller always overwrites<br />projection.be0x74a.io/owned-by to its own bookkeeping value;<br />attempts to set it here are ignored. |  | Optional: \{\} <br /> |


#### Projection



Projection mirrors one Kubernetes object from a source location to one or
more destination namespaces, declaratively and conflict-safely. Source
edits propagate to destinations in ~100 ms via dynamic watches. Destinations
carry a projection.be0x74a.io/owned-by annotation the controller uses to
refuse overwriting resources it did not create.



_Appears in:_
- [ProjectionList](#projectionlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.be0x74a.io/v1` | | |
| `kind` _string_ | `Projection` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ProjectionSpec](#projectionspec)_ |  |  |  |
| `status` _[ProjectionStatus](#projectionstatus)_ |  |  |  |


#### ProjectionList



ProjectionList contains a list of Projection.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `projection.be0x74a.io/v1` | | |
| `kind` _string_ | `ProjectionList` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[Projection](#projection) array_ |  |  |  |


#### ProjectionSpec



ProjectionSpec specifies which source object to mirror, where to write it,
and what metadata overlays to apply.



_Appears in:_
- [Projection](#projection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `source` _[SourceRef](#sourceref)_ | Source is the object to project from. |  |  |
| `destination` _[DestinationRef](#destinationref)_ | Destination controls where the projected object is written. |  | Optional: \{\} <br /> |
| `overlay` _[Overlay](#overlay)_ | Overlay applies metadata patches on top of the projected object. |  | Optional: \{\} <br /> |


#### ProjectionStatus



ProjectionStatus reports the most recent reconcile outcome via three
conditions: SourceResolved, DestinationWritten, and Ready.



_Appears in:_
- [Projection](#projection)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions reflect the current state of the projection. The controller<br />sets type "Ready" to True once the destination has been written, or<br />False with a reason describing why not. |  | Optional: \{\} <br /> |


#### SourceRef



SourceRef identifies the Kubernetes object to project from.



_Appears in:_
- [ProjectionSpec](#projectionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | APIVersion of the source object, e.g. "v1" or "apps/v1". |  | MinLength: 1 <br />Pattern: `^([a-z0-9.-]+/)?v[0-9]+((alpha\|beta)[0-9]+)?$` <br />Required: \{\} <br /> |
| `kind` _string_ | Kind of the source object, e.g. "ConfigMap". |  | MinLength: 1 <br />Pattern: `^[A-Z][A-Za-z0-9]*$` <br />Required: \{\} <br /> |
| `name` _string_ | Name of the source object. DNS-1123 subdomain: lowercase alphanumerics,<br />'-', and '.', up to 253 chars. Matches the permissive form Kubernetes<br />uses for most named objects (ConfigMap, Secret, Deployment, Pod, …). |  | MaxLength: 253 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |
| `namespace` _string_ | Namespace of the source object. |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |


