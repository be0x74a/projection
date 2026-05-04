package main

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// teardown reverses bootstrap. Deletes Projections first (so the controller
// stops reconciling), then sources, then namespaces, then CRDs. Best-effort:
// logs failures but continues.
func teardown(ctx context.Context, c *clients, res *bootstrapResult) {
	projGVR := schema.GroupVersionResource{Group: "projection.sh", Version: "v1", Resource: "projections"}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

	// Delete Projections.
	for _, p := range res.Projections {
		_ = c.dynamic.Resource(projGVR).Namespace(p.ProjNs).
			Delete(ctx, p.ProjName, metav1.DeleteOptions{})
	}
	// Let finalizers run.
	time.Sleep(2 * time.Second)

	// Delete source namespaces (and destination namespaces — both cascade
	// their contents, which removes source and destination CRs).
	for _, ns := range append(append([]string{}, res.SourceNsList...), res.DestNsList...) {
		err := c.dynamic.Resource(nsGVR).Delete(ctx, ns, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// Best-effort; keep going.
			_ = err
		}
	}
	// Delete the Projections namespace itself.
	_ = c.dynamic.Resource(nsGVR).Delete(ctx, res.ProjectionNs, metav1.DeleteOptions{})

	// Delete bench CRDs. The cluster admin can run again and the CRDs get
	// recreated idempotently via installCRDs.
	for i := 0; i < res.Profile.GVKs; i++ {
		crdName := benchPlural(i) + "." + benchGroup
		_ = c.apiext.ApiextensionsV1().CustomResourceDefinitions().
			Delete(ctx, crdName, metav1.DeleteOptions{})
	}
}
