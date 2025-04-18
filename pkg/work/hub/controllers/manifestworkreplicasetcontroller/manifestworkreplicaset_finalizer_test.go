package manifestworkreplicasetcontroller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fakeclient "open-cluster-management.io/api/client/work/clientset/versioned/fake"
	workinformers "open-cluster-management.io/api/client/work/informers/externalversions"
	workapplier "open-cluster-management.io/sdk-go/pkg/apis/work/v1/applier"

	commonhelper "open-cluster-management.io/ocm/pkg/common/helpers"
	helpertest "open-cluster-management.io/ocm/pkg/work/hub/test"
)

// Test finalize reconcile
func TestFinalizeReconcile(t *testing.T) {
	mwrSetTest := helpertest.CreateTestManifestWorkReplicaSet("mwrSet-test", "default", "place-test")
	mw, _ := CreateManifestWork(mwrSetTest, "cluster1", "place-test")
	fakeClient := fakeclient.NewSimpleClientset(mwrSetTest, mw)
	manifestWorkInformerFactory := workinformers.NewSharedInformerFactoryWithOptions(fakeClient, 1*time.Second)
	mwLister := manifestWorkInformerFactory.Work().V1().ManifestWorks().Lister()

	finalizerController := finalizeReconciler{
		workClient:         fakeClient,
		manifestWorkLister: mwLister,
		workApplier:        workapplier.NewWorkApplierWithTypedClient(fakeClient, mwLister),
	}

	// Set manifestWorkReplicaSet delete time AND Set finalizer
	timeNow := metav1.Now()
	mwrSetTest.DeletionTimestamp = &timeNow
	mwrSetTest.Finalizers = append(mwrSetTest.Finalizers, ManifestWorkReplicaSetFinalizer)

	_, _, err := finalizerController.reconcile(context.TODO(), mwrSetTest)
	if err != nil {
		t.Fatal(err)
	}

	updatetSet, err := fakeClient.WorkV1alpha1().ManifestWorkReplicaSets(mwrSetTest.Namespace).Get(context.TODO(), mwrSetTest.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Check mwrSetTest finalizer removed
	if commonhelper.HasFinalizer(updatetSet.Finalizers, ManifestWorkReplicaSetFinalizer) {
		t.Fatal("Finalizer not deleted", mwrSetTest.Finalizers)
	}
}
