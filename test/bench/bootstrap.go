package main

import (
	"context"
	"fmt"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	benchGroup           = "bench.projection.sh"
	benchVersion         = "v1"
	benchAnnotationStamp = "bench.projection.sh/stamp"
	srcNsPrefix          = "bench-src"
	dstNsPrefix          = "bench-dst"
	cpSelectorSrcNs      = "bench-cp-sel-src"
	cpSelectorDstPrefix  = "bench-cp-sel"
	cpSelectorSrcName    = "bench-cp-sel-src-0"
	cpSelectorProjName   = "bench-cp-sel-0"
	cpSelectorLabelKey   = "bench-cp-selector"
	cpSelectorLabelValue = "true"
	cpListSrcNs          = "bench-cp-list-src"
	cpListDstPrefix      = "bench-cp-list"
	cpListSrcName        = "bench-cp-list-src-0"
	cpListProjName       = "bench-cp-list-0"
	projectionGroup      = "projection.sh"
	projectionVersion    = "v1"
	projectionResource   = "projections"
	clusterProjResource  = "clusterprojections"
)

// projGVR is the GVR for the namespaced Projection CR.
var projGVR = schema.GroupVersionResource{
	Group: projectionGroup, Version: projectionVersion, Resource: projectionResource,
}

// cprojGVR is the GVR for the cluster-scoped ClusterProjection CR.
var cprojGVR = schema.GroupVersionResource{
	Group: projectionGroup, Version: projectionVersion, Resource: clusterProjResource,
}

// nsGVR is the GVR for core Namespace.
var nsGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// clients is the minimal set of Kubernetes clients the harness uses.
type clients struct {
	kube    kubernetes.Interface
	dynamic dynamic.Interface
	apiext  apiextclient.Interface
	host    string // apiserver URL (for identification only)
}

func buildClients(kubeconfig string) (*clients, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %q: %w", kubeconfig, err)
	}
	// client-go defaults to QPS=5, Burst=10 when unset, which throttles the
	// harness's own observation loop: each sample consumes 2 tokens (one
	// PATCH, one Get), steady state is 400ms/sample, and it shows up in
	// e2e_p50/p95/p99 as a spurious ~400ms floor. Setting these high enough
	// that the harness never throttles itself — we're a local diagnostic
	// tool, not a production client.
	cfg.QPS = 500
	cfg.Burst = 1000
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kube client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building apiextensions client: %w", err)
	}
	return &clients{kube: kube, dynamic: dyn, apiext: apiext, host: cfg.Host}, nil
}

// benchKind returns the templated Kind string for GVK index i.
func benchKind(i int) string { return fmt.Sprintf("BenchObject%d", i) }

// benchPlural returns the templated plural (lowercase kind).
func benchPlural(i int) string { return fmt.Sprintf("benchobject%ds", i) }

// gvr returns the GVR for bench GVK index i.
func gvr(i int) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: benchGroup, Version: benchVersion, Resource: benchPlural(i)}
}

// installCRDs installs N generic CRDs named bench.projection.sh/v1
// BenchObjectN with a permissive schema. Idempotent.
func installCRDs(ctx context.Context, c *clients, nGVKs int) error {
	for i := 0; i < nGVKs; i++ {
		crd := &apiextv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s.%s", benchPlural(i), benchGroup),
			},
			Spec: apiextv1.CustomResourceDefinitionSpec{
				Group: benchGroup,
				Names: apiextv1.CustomResourceDefinitionNames{
					Plural:   benchPlural(i),
					Singular: fmt.Sprintf("benchobject%d", i),
					Kind:     benchKind(i),
					ListKind: benchKind(i) + "List",
				},
				Scope: apiextv1.NamespaceScoped,
				Versions: []apiextv1.CustomResourceDefinitionVersion{{
					Name: benchVersion, Served: true, Storage: true,
					Schema: &apiextv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextv1.JSONSchemaProps{
							Type:                   "object",
							XPreserveUnknownFields: ptr(true),
						},
					},
				}},
			},
		}
		_, err := c.apiext.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("creating CRD %s: %w", crd.Name, err)
		}
	}
	// Let the apiserver establish the CRDs before we create instances.
	time.Sleep(3 * time.Second)
	return nil
}

func ptr[T any](v T) *T { return &v }

// ensureNamespace creates a namespace (with optional extra labels) if it
// doesn't already exist. Idempotent.
func ensureNamespace(ctx context.Context, c *clients, name string, extraLabels map[string]string) error {
	_, err := c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	labels := map[string]interface{}{"bench": "true"}
	for k, v := range extraLabels {
		labels[k] = v
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": labels,
		},
	}}
	_, err = c.dynamic.Resource(nsGVR).Create(ctx, u, metav1.CreateOptions{})
	return err
}

// createSource creates one source object in the given namespace. Idempotent:
// returns nil when an object with the same name already exists.
func createSource(ctx context.Context, c *clients, gvkIdx int, srcNs, name string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: benchGroup, Version: benchVersion, Kind: benchKind(gvkIdx)})
	obj.SetNamespace(srcNs)
	obj.SetName(name)
	obj.SetAnnotations(map[string]string{
		"projection.sh/projectable": "true",
	})
	obj.Object["spec"] = map[string]interface{}{"data": "seed"}
	_, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(srcNs).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// buildSourceRef returns the v0.3 SourceRef map (group/version/kind/namespace/
// name) for use as `spec.source` in a Projection or ClusterProjection.
func buildSourceRef(srcNs, srcName, srcKind string) map[string]interface{} {
	return map[string]interface{}{
		"group":     benchGroup,
		"version":   benchVersion,
		"kind":      srcKind,
		"namespace": srcNs,
		"name":      srcName,
	}
}

// buildNamespacedProjection assembles the v0.3 namespaced Projection
// unstructured. Per v0.3 the destination namespace IS the Projection's own
// namespace, so `projectionNs` doubles as the destination. The destination
// name defaults to source name when unset; callers always pass the source
// name explicitly for round-trip clarity. Exposed as a separate function so
// unit tests can verify the v0.3 shape without an apiserver.
func buildNamespacedProjection(projectionNs, projName, srcNs, srcName, srcKind string) *unstructured.Unstructured {
	proj := &unstructured.Unstructured{}
	proj.SetGroupVersionKind(schema.GroupVersionKind{Group: projectionGroup, Version: projectionVersion, Kind: "Projection"})
	proj.SetNamespace(projectionNs)
	proj.SetName(projName)
	proj.Object["spec"] = map[string]interface{}{
		"source": buildSourceRef(srcNs, srcName, srcKind),
		"destination": map[string]interface{}{
			"name": srcName,
		},
	}
	return proj
}

// createNamespacedProjection creates a v0.3 namespaced Projection CR.
func createNamespacedProjection(ctx context.Context, c *clients, projectionNs, projName, srcNs, srcName, srcKind string) error {
	proj := buildNamespacedProjection(projectionNs, projName, srcNs, srcName, srcKind)
	_, err := c.dynamic.Resource(projGVR).Namespace(projectionNs).
		Create(ctx, proj, metav1.CreateOptions{})
	return err
}

// buildClusterProjectionSelector assembles a ClusterProjection unstructured
// using a namespaceSelector destination.
func buildClusterProjectionSelector(cpName, srcNs, srcName, srcKind, labelKey, labelValue string) *unstructured.Unstructured {
	cp := &unstructured.Unstructured{}
	cp.SetGroupVersionKind(schema.GroupVersionKind{Group: projectionGroup, Version: projectionVersion, Kind: "ClusterProjection"})
	cp.SetName(cpName)
	cp.Object["spec"] = map[string]interface{}{
		"source": buildSourceRef(srcNs, srcName, srcKind),
		"destination": map[string]interface{}{
			"namespaceSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{labelKey: labelValue},
			},
			"name": srcName,
		},
	}
	return cp
}

// createClusterProjectionSelector creates a cluster-scoped ClusterProjection
// using a namespaceSelector destination. Idempotent: cluster-scoped CRs
// outlive the namespaces that source/destination CRs live in, so a Ctrl-C'd
// or partially-failed previous run can leak the deterministic-named CP, and
// the next run must be able to re-create cleanly.
func createClusterProjectionSelector(ctx context.Context, c *clients, cpName, srcNs, srcName, srcKind, labelKey, labelValue string) error {
	cp := buildClusterProjectionSelector(cpName, srcNs, srcName, srcKind, labelKey, labelValue)
	_, err := c.dynamic.Resource(cprojGVR).Create(ctx, cp, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// buildClusterProjectionList assembles a ClusterProjection unstructured
// using an explicit namespaces list destination.
func buildClusterProjectionList(cpName, srcNs, srcName, srcKind string, namespaces []string) *unstructured.Unstructured {
	nsAny := make([]interface{}, 0, len(namespaces))
	for _, n := range namespaces {
		nsAny = append(nsAny, n)
	}
	cp := &unstructured.Unstructured{}
	cp.SetGroupVersionKind(schema.GroupVersionKind{Group: projectionGroup, Version: projectionVersion, Kind: "ClusterProjection"})
	cp.SetName(cpName)
	cp.Object["spec"] = map[string]interface{}{
		"source": buildSourceRef(srcNs, srcName, srcKind),
		"destination": map[string]interface{}{
			"namespaces": nsAny,
			"name":       srcName,
		},
	}
	return cp
}

// createClusterProjectionList creates a cluster-scoped ClusterProjection
// using an explicit namespaces list destination. Idempotent for the same
// reason as createClusterProjectionSelector.
func createClusterProjectionList(ctx context.Context, c *clients, cpName, srcNs, srcName, srcKind string, namespaces []string) error {
	cp := buildClusterProjectionList(cpName, srcNs, srcName, srcKind, namespaces)
	_, err := c.dynamic.Resource(cprojGVR).Create(ctx, cp, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// projectionRef tracks one namespaced Projection CR plus its source/destination
// coordinates. Used by single-target measurement and teardown.
type projectionRef struct {
	GVKIdx   int
	SrcNs    string
	SrcName  string
	DstNs    string // == ProjNs in v0.3 (destination is implicitly own ns)
	ProjNs   string
	ProjName string
}

// clusterProjectionRef tracks one cluster-scoped ClusterProjection CR plus
// its source coordinates. Destination set is carried separately on
// bootstrapResult since selector vs list are tracked the same way for
// fan-out measurement.
type clusterProjectionRef struct {
	GVKIdx  int
	SrcNs   string
	SrcName string
	CPName  string
}

// bootstrapResult records what each measurement and teardown path needs.
type bootstrapResult struct {
	Profile        Profile
	NPRefs         []projectionRef       // for NP measurement
	CPSelectorRef  *clusterProjectionRef // nil when no CP-selector shape
	CPSelectorDsts []string              // dest ns set for the CP-selector instance
	CPListRef      *clusterProjectionRef // nil when no CP-list shape
	CPListDsts     []string              // dest ns set for the CP-list instance
	AllSrcNs       []string              // for teardown: every source ns we created
	AllDstNs       []string              // for teardown: every dest ns we created
}

// bootstrap sets up the cluster for the given profile, layering up to three
// independent topology paths. CRDs are always installed. Returns the
// bootstrapResult so measurement / teardown can find what to scrape and
// destroy.
func bootstrap(ctx context.Context, c *clients, p Profile) (*bootstrapResult, error) {
	res := &bootstrapResult{Profile: p}

	if err := installCRDs(ctx, c, p.GVKs); err != nil {
		return nil, err
	}

	if p.NamespacedProjections > 0 {
		if err := bootstrapNP(ctx, c, p, res); err != nil {
			return nil, err
		}
	}
	if p.SelectorNamespaces > 0 {
		if err := bootstrapCPSelector(ctx, c, p, res); err != nil {
			return nil, err
		}
	}
	if p.ListNamespaces > 0 {
		if err := bootstrapCPList(ctx, c, p, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// bootstrapNP creates the NP topology: source namespaces, destination
// namespaces, source CRs, and namespaced Projection CRs (one per
// p.NamespacedProjections). Each Projection lives IN its destination
// namespace (v0.3 invariant).
func bootstrapNP(ctx context.Context, c *clients, p Profile, res *bootstrapResult) error {
	if p.Namespaces < 1 {
		return fmt.Errorf("NP profile requires Namespaces >= 1, got %d", p.Namespaces)
	}
	srcNsList := make([]string, 0, p.Namespaces)
	dstNsList := make([]string, 0, p.Namespaces)
	for j := 0; j < p.Namespaces; j++ {
		srcNs := fmt.Sprintf("%s-%d", srcNsPrefix, j)
		dstNs := fmt.Sprintf("%s-%d", dstNsPrefix, j)
		if err := ensureNamespace(ctx, c, srcNs, nil); err != nil {
			return err
		}
		if err := ensureNamespace(ctx, c, dstNs, nil); err != nil {
			return err
		}
		srcNsList = append(srcNsList, srcNs)
		dstNsList = append(dstNsList, dstNs)
	}
	res.AllSrcNs = append(res.AllSrcNs, srcNsList...)
	res.AllDstNs = append(res.AllDstNs, dstNsList...)

	// Round-robin distribute the NP CR set across (gvk, srcNs/dstNs).
	for i := 0; i < p.NamespacedProjections; i++ {
		gvkIdx := i % p.GVKs
		nsIdx := i % p.Namespaces
		srcNs := srcNsList[nsIdx]
		dstNs := dstNsList[nsIdx]
		srcName := fmt.Sprintf("src-%d", i)
		projName := fmt.Sprintf("proj-%d", i)
		if err := createSource(ctx, c, gvkIdx, srcNs, srcName); err != nil {
			return fmt.Errorf("source %d: %w", i, err)
		}
		// In v0.3, the destination namespace IS the Projection's own
		// namespace, so the Projection CR itself lives in dstNs.
		if err := createNamespacedProjection(ctx, c, dstNs, projName, srcNs, srcName, benchKind(gvkIdx)); err != nil {
			return fmt.Errorf("projection %d: %w", i, err)
		}
		res.NPRefs = append(res.NPRefs, projectionRef{
			GVKIdx: gvkIdx, SrcNs: srcNs, SrcName: srcName, DstNs: dstNs,
			ProjNs: dstNs, ProjName: projName,
		})
	}
	return nil
}

// bootstrapCPSelector creates the CP-selector topology: a single source
// namespace + source CR, p.SelectorNamespaces label-matched destination
// namespaces, and one ClusterProjection with namespaceSelector destination.
//
// Both CP paths (selector and list) intentionally pin to GVK index 0 with a
// single source CR. CP fan-out behavior — selector matching, namespace
// iteration, list-driven distribution — is independent of source-GVK count;
// exercising multiple GVKs is the NP path's job. mixed-* profiles drive
// multi-GVK behavior through NP and fan-out behavior through CP without
// redundancy.
func bootstrapCPSelector(ctx context.Context, c *clients, p Profile, res *bootstrapResult) error {
	if err := ensureNamespace(ctx, c, cpSelectorSrcNs, nil); err != nil {
		return err
	}
	res.AllSrcNs = append(res.AllSrcNs, cpSelectorSrcNs)
	if err := createSource(ctx, c, 0, cpSelectorSrcNs, cpSelectorSrcName); err != nil {
		return fmt.Errorf("creating cp-selector source: %w", err)
	}
	dstSet := make([]string, 0, p.SelectorNamespaces)
	for j := 0; j < p.SelectorNamespaces; j++ {
		dstNs := fmt.Sprintf("%s-%d", cpSelectorDstPrefix, j)
		if err := ensureNamespace(ctx, c, dstNs, map[string]string{cpSelectorLabelKey: cpSelectorLabelValue}); err != nil {
			return err
		}
		dstSet = append(dstSet, dstNs)
	}
	res.AllDstNs = append(res.AllDstNs, dstSet...)
	if err := createClusterProjectionSelector(ctx, c,
		cpSelectorProjName, cpSelectorSrcNs, cpSelectorSrcName, benchKind(0),
		cpSelectorLabelKey, cpSelectorLabelValue); err != nil {
		return fmt.Errorf("creating cp-selector ClusterProjection: %w", err)
	}
	res.CPSelectorRef = &clusterProjectionRef{
		GVKIdx: 0, SrcNs: cpSelectorSrcNs, SrcName: cpSelectorSrcName, CPName: cpSelectorProjName,
	}
	res.CPSelectorDsts = dstSet
	return nil
}

// bootstrapCPList creates the CP-list topology: a single source namespace +
// source CR, p.ListNamespaces explicit destination namespaces, and one
// ClusterProjection with an explicit namespaces list destination.
func bootstrapCPList(ctx context.Context, c *clients, p Profile, res *bootstrapResult) error {
	if err := ensureNamespace(ctx, c, cpListSrcNs, nil); err != nil {
		return err
	}
	res.AllSrcNs = append(res.AllSrcNs, cpListSrcNs)
	if err := createSource(ctx, c, 0, cpListSrcNs, cpListSrcName); err != nil {
		return fmt.Errorf("creating cp-list source: %w", err)
	}
	dstSet := make([]string, 0, p.ListNamespaces)
	for j := 0; j < p.ListNamespaces; j++ {
		dstNs := fmt.Sprintf("%s-%d", cpListDstPrefix, j)
		if err := ensureNamespace(ctx, c, dstNs, nil); err != nil {
			return err
		}
		dstSet = append(dstSet, dstNs)
	}
	res.AllDstNs = append(res.AllDstNs, dstSet...)
	if err := createClusterProjectionList(ctx, c,
		cpListProjName, cpListSrcNs, cpListSrcName, benchKind(0), dstSet); err != nil {
		return fmt.Errorf("creating cp-list ClusterProjection: %w", err)
	}
	res.CPListRef = &clusterProjectionRef{
		GVKIdx: 0, SrcNs: cpListSrcNs, SrcName: cpListSrcName, CPName: cpListProjName,
	}
	res.CPListDsts = dstSet
	return nil
}
