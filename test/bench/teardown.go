package main

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// teardown reverses bootstrap. Deletes Projection / ClusterProjection CRs
// first (so the controller stops reconciling), then namespaces (which
// cascade-delete contents), then CRDs. Best-effort on every individual
// Delete (logs failures but continues), but synchronously waits for the
// resources to be fully NotFound before returning so the next profile
// in --profile=full sequences starts from a clean slate. Without that
// final wait, async finalizers leave Terminating shells around and the
// next bootstrap races them: namespace Get-succeeds-then-disappears,
// CRD Create-AlreadyExists-then-disappears, etc.
func teardown(ctx context.Context, c *clients, res *bootstrapResult) {
	// Delete namespaced Projections.
	for _, p := range res.NPRefs {
		_ = c.dynamic.Resource(projGVR).Namespace(p.ProjNs).
			Delete(ctx, p.ProjName, metav1.DeleteOptions{})
	}
	// Delete the ClusterProjection CRs (cluster-scoped, no namespace arg).
	cpNames := make([]string, 0, 2)
	if res.CPSelectorRef != nil {
		_ = c.dynamic.Resource(cprojGVR).Delete(ctx, res.CPSelectorRef.CPName, metav1.DeleteOptions{})
		cpNames = append(cpNames, res.CPSelectorRef.CPName)
	}
	if res.CPListRef != nil {
		_ = c.dynamic.Resource(cprojGVR).Delete(ctx, res.CPListRef.CPName, metav1.DeleteOptions{})
		cpNames = append(cpNames, res.CPListRef.CPName)
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
	crdNames := make([]string, 0, res.Profile.GVKs)
	for i := 0; i < res.Profile.GVKs; i++ {
		crdName := benchPlural(i) + "." + benchGroup
		_ = c.apiext.ApiextensionsV1().CustomResourceDefinitions().
			Delete(ctx, crdName, metav1.DeleteOptions{})
		crdNames = append(crdNames, crdName)
	}

	// Synchronous wait: poll until every namespace, CRD, and CP we deleted
	// is fully NotFound. Bounded at 120s; on timeout we proceed silently
	// (the next bootstrap will surface the issue if state is genuinely
	// stuck). The wait is cheap when there's nothing to wait for — first
	// poll iteration sees everything NotFound and returns.
	waitDeleted(ctx, c, allNs, crdNames, cpNames)
}

// waitDeleted polls until every named namespace, CRD, and ClusterProjection
// is observed NotFound, or the 120s deadline is reached. Returns silently
// in either case — teardown is best-effort by contract.
func waitDeleted(ctx context.Context, c *clients, namespaces, crdNames, cpNames []string) {
	deadline := time.Now().Add(120 * time.Second)
	for {
		anyRemaining := false
		for _, ns := range namespaces {
			_, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				anyRemaining = true
				break
			}
		}
		if !anyRemaining {
			for _, name := range crdNames {
				_, err := c.apiext.ApiextensionsV1().CustomResourceDefinitions().
					Get(ctx, name, metav1.GetOptions{})
				if !apierrors.IsNotFound(err) {
					anyRemaining = true
					break
				}
			}
		}
		if !anyRemaining {
			for _, name := range cpNames {
				_, err := c.dynamic.Resource(cprojGVR).Get(ctx, name, metav1.GetOptions{})
				if !apierrors.IsNotFound(err) {
					anyRemaining = true
					break
				}
			}
		}
		if !anyRemaining {
			return
		}
		if time.Now().After(deadline) {
			return // best-effort timeout; next bootstrap will surface stuck state
		}
		time.Sleep(1 * time.Second)
	}
}
