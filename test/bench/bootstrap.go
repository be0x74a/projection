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
	benchGroup           = "bench.projection.be0x74a.io"
	benchVersion         = "v1"
	benchAnnotationStamp = "bench.projection.be0x74a.io/stamp"
	srcNsPrefix          = "bench-src"
	dstNsPrefix          = "bench-dst"
)

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

// installCRDs installs N generic CRDs named bench.projection.be0x74a.io/v1
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

// ensureNamespace creates a namespace if it doesn't exist.
func ensureNamespace(ctx context.Context, c *clients, name string) error {
	_, err := c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	ns := map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{"bench": "true"},
		},
	}
	u := &unstructured.Unstructured{Object: ns}
	_, err = c.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Create(ctx, u, metav1.CreateOptions{})
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
		"projection.be0x74a.io/projectable": "true",
	})
	obj.Object["spec"] = map[string]interface{}{"data": "seed"}
	_, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(srcNs).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// createProjection creates a projection pointing at the given source, with a
// single-namespace destination. projectionNs must already exist.
func createProjection(ctx context.Context, c *clients, projectionNs, projName, srcNs, srcName, srcKind, dstNs string) error {
	proj := &unstructured.Unstructured{}
	proj.SetGroupVersionKind(schema.GroupVersionKind{Group: "projection.be0x74a.io", Version: "v1", Kind: "Projection"})
	proj.SetNamespace(projectionNs)
	proj.SetName(projName)
	proj.Object["spec"] = map[string]interface{}{
		"source": map[string]interface{}{
			"apiVersion": benchGroup + "/" + benchVersion,
			"kind":       srcKind,
			"namespace":  srcNs,
			"name":       srcName,
		},
		"destination": map[string]interface{}{
			"namespace": dstNs,
			"name":      srcName,
		},
	}
	_, err := c.dynamic.Resource(schema.GroupVersionResource{
		Group: "projection.be0x74a.io", Version: "v1", Resource: "projections",
	}).Namespace(projectionNs).Create(ctx, proj, metav1.CreateOptions{})
	return err
}

// bootstrap sets up the cluster for the given profile: CRDs, namespaces,
// source objects, projections. Returns a bootstrapResult so measurement /
// teardown know what to scrape and destroy.
type bootstrapResult struct {
	Profile      Profile
	SourceNsList []string
	DestNsList   []string
	ProjectionNs string
	// Each entry: (gvk idx, src ns, src name, dst ns, proj name)
	Projections []projectionRef
}

type projectionRef struct {
	GVKIdx   int
	SrcNs    string
	SrcName  string
	DstNs    string
	ProjNs   string
	ProjName string
}

func bootstrap(ctx context.Context, c *clients, p Profile) (*bootstrapResult, error) {
	// All Projection CRs live in one dedicated namespace.
	projectionNs := "bench-projections"
	res := &bootstrapResult{Profile: p, ProjectionNs: projectionNs}

	if err := installCRDs(ctx, c, p.GVKs); err != nil {
		return nil, err
	}
	if err := ensureNamespace(ctx, c, projectionNs); err != nil {
		return nil, err
	}

	// Namespaces: for regular profiles we create N source + N dest namespaces.
	// For selector: 1 source ns, SelectorNamespaces dest namespaces labeled for the selector.
	if p.SelectorNamespaces > 0 {
		srcNs := srcNsPrefix + "-0"
		if err := ensureNamespace(ctx, c, srcNs); err != nil {
			return nil, err
		}
		res.SourceNsList = []string{srcNs}
		for j := 0; j < p.SelectorNamespaces; j++ {
			dstNs := fmt.Sprintf("%s-sel-%d", dstNsPrefix, j)
			if err := ensureNamespaceWithLabel(ctx, c, dstNs, "bench-selector", "true"); err != nil {
				return nil, err
			}
			res.DestNsList = append(res.DestNsList, dstNs)
		}
	} else {
		for j := 0; j < p.Namespaces; j++ {
			srcNs := fmt.Sprintf("%s-%d", srcNsPrefix, j)
			dstNs := fmt.Sprintf("%s-%d", dstNsPrefix, j)
			if err := ensureNamespace(ctx, c, srcNs); err != nil {
				return nil, err
			}
			if err := ensureNamespace(ctx, c, dstNs); err != nil {
				return nil, err
			}
			res.SourceNsList = append(res.SourceNsList, srcNs)
			res.DestNsList = append(res.DestNsList, dstNs)
		}
	}

	// Source objects + Projections.
	if p.SelectorNamespaces > 0 {
		// Single source object, single Projection with a namespaceSelector.
		srcName := "bench-src-0"
		if err := createSource(ctx, c, 0, res.SourceNsList[0], srcName); err != nil {
			return nil, fmt.Errorf("creating source: %w", err)
		}
		projName := "bench-proj-0"
		if err := createSelectorProjection(ctx, c, projectionNs, projName,
			res.SourceNsList[0], srcName, benchKind(0)); err != nil {
			return nil, fmt.Errorf("creating selector projection: %w", err)
		}
		res.Projections = append(res.Projections, projectionRef{
			GVKIdx: 0, SrcNs: res.SourceNsList[0], SrcName: srcName,
			ProjNs: projectionNs, ProjName: projName,
		})
		return res, nil
	}

	// Round-robin distribute across (gvk, srcNs).
	for i := 0; i < p.Projections; i++ {
		gvkIdx := i % p.GVKs
		nsIdx := i % p.Namespaces
		srcNs := res.SourceNsList[nsIdx]
		dstNs := res.DestNsList[nsIdx]
		srcName := fmt.Sprintf("src-%d", i)
		projName := fmt.Sprintf("proj-%d", i)
		if err := createSource(ctx, c, gvkIdx, srcNs, srcName); err != nil {
			return nil, fmt.Errorf("source %d: %w", i, err)
		}
		if err := createProjection(ctx, c, projectionNs, projName, srcNs, srcName, benchKind(gvkIdx), dstNs); err != nil {
			return nil, fmt.Errorf("projection %d: %w", i, err)
		}
		res.Projections = append(res.Projections, projectionRef{
			GVKIdx: gvkIdx, SrcNs: srcNs, SrcName: srcName, DstNs: dstNs,
			ProjNs: projectionNs, ProjName: projName,
		})
	}
	return res, nil
}

func ensureNamespaceWithLabel(ctx context.Context, c *clients, name, k, v string) error {
	_, err := c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{k: v, "bench": "true"},
		},
	}}
	_, err = c.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Create(ctx, u, metav1.CreateOptions{})
	return err
}

func createSelectorProjection(ctx context.Context, c *clients, projNs, projName, srcNs, srcName, srcKind string) error {
	proj := &unstructured.Unstructured{}
	proj.SetGroupVersionKind(schema.GroupVersionKind{Group: "projection.be0x74a.io", Version: "v1", Kind: "Projection"})
	proj.SetNamespace(projNs)
	proj.SetName(projName)
	proj.Object["spec"] = map[string]interface{}{
		"source": map[string]interface{}{
			"apiVersion": benchGroup + "/" + benchVersion,
			"kind":       srcKind,
			"namespace":  srcNs,
			"name":       srcName,
		},
		"destination": map[string]interface{}{
			"namespaceSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"bench-selector": "true"},
			},
			"name": srcName,
		},
	}
	_, err := c.dynamic.Resource(schema.GroupVersionResource{
		Group: "projection.be0x74a.io", Version: "v1", Resource: "projections",
	}).Namespace(projNs).Create(ctx, proj, metav1.CreateOptions{})
	return err
}
