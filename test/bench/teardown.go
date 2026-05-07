package main

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// teardown reverses bootstrap. Deletes Projection / ClusterProjection CRs
// first (so the controller stops reconciling), then namespaces (which
// cascade-delete contents), then CRDs. Best-effort throughout: logs failures
// but continues.
func teardown(ctx context.Context, c *clients, res *bootstrapResult) {
	// Delete namespaced Projections.
	for _, p := range res.NPRefs {
		_ = c.dynamic.Resource(projGVR).Namespace(p.ProjNs).
			Delete(ctx, p.ProjName, metav1.DeleteOptions{})
	}
	// Delete the ClusterProjection CRs (cluster-scoped, no namespace arg).
	if res.CPSelectorRef != nil {
		_ = c.dynamic.Resource(cprojGVR).Delete(ctx, res.CPSelectorRef.CPName, metav1.DeleteOptions{})
	}
	if res.CPListRef != nil {
		_ = c.dynamic.Resource(cprojGVR).Delete(ctx, res.CPListRef.CPName, metav1.DeleteOptions{})
	}

	// Let finalizers run.
	time.Sleep(2 * time.Second)

	// Delete every namespace we provisioned. Namespace deletion cascades to
	// the source CRs, projected destinations, and any leftover Projection
	// CRs that lived in those namespaces.
	allNs := make([]string, 0, len(res.AllSrcNs)+len(res.AllDstNs))
	allNs = append(allNs, res.AllSrcNs...)
	allNs = append(allNs, res.AllDstNs...)
	for _, ns := range allNs {
		err := c.dynamic.Resource(nsGVR).Delete(ctx, ns, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// Best-effort; keep going.
			_ = err
		}
	}

	// Delete bench CRDs. The cluster admin can run again and the CRDs get
	// recreated idempotently via installCRDs.
	for i := 0; i < res.Profile.GVKs; i++ {
		crdName := benchPlural(i) + "." + benchGroup
		_ = c.apiext.ApiextensionsV1().CustomResourceDefinitions().
			Delete(ctx, crdName, metav1.DeleteOptions{})
	}
}
