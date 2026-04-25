/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	projectionv1 "github.com/be0x74a/projection/api/v1"
)

// widgetCRD is a two-version (v1alpha1 + v1, both served, conversion=None,
// storage=v1) custom CRD we install at test time to exercise the
// preferred-version resolution path for non-core groups.
//
// The RESTMapper we build per reconciler via apiutil.NewDynamicRESTMapper
// is not pre-warmed and will discover this CRD on first lookup, so
// runtime install in a BeforeEach is sufficient — no need to rebuild
// the manager or pre-load the CRD in BeforeSuite.
func widgetCRD() *apiextensionsv1.CustomResourceDefinition {
	openAPIV3Schema := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"spec": {
				Type:                   "object",
				XPreserveUnknownFields: ptr.To(true),
			},
		},
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "widgets",
				Singular: "widget",
				Kind:     "Widget",
				ListKind: "WidgetList",
			},
			Conversion: &apiextensionsv1.CustomResourceConversion{
				Strategy: apiextensionsv1.NoneConverter,
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: false,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: openAPIV3Schema,
					},
				},
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: openAPIV3Schema,
					},
				},
			},
		},
	}
}

var _ = Describe("Preferred-version resolution against a multi-version CRD", func() {
	var (
		r       *ProjectionReconciler
		crd     *apiextensionsv1.CustomResourceDefinition
		srcNS   string
		dstNS   string
		projKey types.NamespacedName
	)

	BeforeEach(func() {
		// Install the two-served-version Widget CRD and wait for it to
		// become Established before we exercise the resolver. The
		// reconciler (and the fresh RESTMapper it builds) comes next,
		// so it sees the CRD on first use.
		crd = widgetCRD()
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		Eventually(func(g Gomega) {
			got := &apiextensionsv1.CustomResourceDefinition{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: crd.Name}, got)).To(Succeed())
			established := false
			for _, c := range got.Status.Conditions {
				if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
					established = true
					break
				}
			}
			g.Expect(established).To(BeTrue(), "Widget CRD must reach Established=True")
		}, "10s", "200ms").Should(Succeed())

		r = newReconciler()

		srcNS = uniqueNS("widget-src")
		dstNS = uniqueNS("widget-dst")
		ensureNamespace(srcNS)
		ensureNamespace(dstNS)
	})

	AfterEach(func() {
		// Clean up the CRD so subsequent specs see a pristine envtest.
		// Instances are namespaced and disappear when the CRD goes.
		deleteProjection(projKey)
		_ = k8sClient.Delete(ctx, crd.DeepCopy())
	})

	It("picks the storage version (v1) when source.apiVersion is example.com/*", func() {
		// Create a Widget instance via v1.
		widget := &unstructured.Unstructured{}
		widget.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "example.com", Version: "v1", Kind: "Widget",
		})
		widget.SetName("widget-src")
		widget.SetNamespace(srcNS)
		widget.SetAnnotations(map[string]string{projectableAnnotation: "true"})
		widget.Object["spec"] = map[string]interface{}{"data": "hello"}
		Expect(k8sClient.Create(ctx, widget)).To(Succeed())

		// Projection with unpinned apiVersion — the RESTMapper should
		// resolve example.com/* to the preferred (storage) version v1.
		projKey = types.NamespacedName{Name: "widget-proj", Namespace: srcNS}
		proj := &projectionv1.Projection{
			ObjectMeta: metav1.ObjectMeta{Name: projKey.Name, Namespace: projKey.Namespace},
			Spec: projectionv1.ProjectionSpec{
				Source: projectionv1.SourceRef{
					APIVersion: "example.com/*",
					Kind:       "Widget",
					Name:       "widget-src",
					Namespace:  srcNS,
				},
				Destination: projectionv1.DestinationRef{Namespace: dstNS},
			},
		}
		Expect(k8sClient.Create(ctx, proj)).To(Succeed())

		reconcileOnce(r, projKey)

		// Destination exists in the dst namespace.
		dst := &unstructured.Unstructured{}
		dst.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "example.com", Version: "v1", Kind: "Widget",
		})
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "widget-src", Namespace: dstNS},
			dst)).To(Succeed())
		Expect(dst.GetAnnotations()).To(HaveKeyWithValue(
			ownedByAnnotation, projKey.Namespace+"/"+projKey.Name))

		// SourceResolved condition reports v1 as the picked version.
		got := &projectionv1.Projection{}
		Expect(k8sClient.Get(ctx, projKey, got)).To(Succeed())
		sr := apimeta.FindStatusCondition(got.Status.Conditions, "SourceResolved")
		Expect(sr).ToNot(BeNil())
		Expect(sr.Status).To(Equal(metav1.ConditionTrue))
		Expect(sr.Message).To(Equal("resolved example.com/Widget to preferred version v1"))
	})
})
