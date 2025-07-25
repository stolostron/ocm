package util

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	operatorclientset "open-cluster-management.io/api/client/operator/clientset/versioned"
	workclientset "open-cluster-management.io/api/client/work/clientset/versioned"
	workapiv1 "open-cluster-management.io/api/work/v1"
)

const (
	eventuallyTimeout  = 60 // seconds
	eventuallyInterval = 1  // seconds
)

func AssertKlusterletCondition(
	name string, operatorClient operatorclientset.Interface, expectedType, expectedReason string, expectedWorkStatus metav1.ConditionStatus) {
	gomega.Eventually(func() error {
		klusterlet, err := operatorClient.OperatorV1().Klusterlets().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// check work status condition
		if !HasCondition(klusterlet.Status.Conditions, expectedType, expectedReason, expectedWorkStatus) {
			return fmt.Errorf("expect have type %s with reason %s and status %s, but got %v",
				expectedType, expectedReason, expectedWorkStatus, klusterlet.Status.Conditions)
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

func AssertClusterManagerCondition(
	name string, operatorClient operatorclientset.Interface, expectedType, expectedReason string, expectedWorkStatus metav1.ConditionStatus) {
	gomega.Eventually(func() error {
		clusterManager, err := operatorClient.OperatorV1().ClusterManagers().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// check work status condition
		if !HasCondition(clusterManager.Status.Conditions, expectedType, expectedReason, expectedWorkStatus) {
			return fmt.Errorf("expect have type %s with reason %s and status %s, but got %v",
				expectedType, expectedReason, expectedWorkStatus, clusterManager.Status.Conditions)
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

func AssertWorkCondition(namespace, name string, workClient workclientset.Interface, expectedType string, expectedWorkStatus metav1.ConditionStatus,
	expectedManifestStatuses []metav1.ConditionStatus, eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() error {
		work, err := workClient.WorkV1().ManifestWorks(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// check manifest status conditions
		if ok := HaveManifestCondition(work.Status.ResourceStatus.Manifests, expectedType, expectedManifestStatuses); !ok {
			return fmt.Errorf("condition %s does not exist, got %v ", expectedType, work.Status.ResourceStatus.Manifests)
		}

		// check work status condition
		actualCond := meta.FindStatusCondition(work.Status.Conditions, expectedType)
		if actualCond == nil {
			return fmt.Errorf("Cannot find expected condition %s", expectedType)
		}
		if work.Generation != actualCond.ObservedGeneration {
			return fmt.Errorf(
				"Generation of condition %d does not match work condition %d",
				actualCond.ObservedGeneration, work.Generation)
		}
		if actualCond.Status == expectedWorkStatus {
			return nil
		}
		return fmt.Errorf("status of type %s does not match", expectedType)
	}, eventuallyTimeout, eventuallyInterval).Should(gomega.Succeed())
}

func AssertWorkGeneration(namespace, name string, workClient workclientset.Interface, expectedType string, eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() error {
		work, err := workClient.WorkV1().ManifestWorks(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// check manifest status conditions
		condition := meta.FindStatusCondition(work.Status.Conditions, expectedType)
		if condition == nil {
			return fmt.Errorf("condition is nil")
		}

		if condition.ObservedGeneration != work.Generation {
			return fmt.Errorf("generation not equal: observedGeneration: %v, generation: %v",
				condition.ObservedGeneration, work.Generation)
		}

		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

// AssertWorkDeleted check if work is deleted
func AssertWorkDeleted(namespace, name, appliedManifestWorkName string, manifests []workapiv1.Manifest,
	workClient, spokeWorkClient workclientset.Interface, spokeKubeClient kubernetes.Interface,
	eventuallyTimeout, eventuallyInterval int) {
	// wait for deletion of manifest work
	gomega.Eventually(func() error {
		_, err := workClient.WorkV1().ManifestWorks(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("work %s in namespace %s still exists", name, namespace)
	}, eventuallyTimeout, eventuallyInterval).Should(gomega.Succeed())

	// wait for deletion of appliedmanifestwork
	AssertAppliedManifestWorkDeleted(appliedManifestWorkName, spokeWorkClient, eventuallyTimeout, eventuallyInterval)

	// Once manifest work is deleted, all applied resources should have already been deleted too
	for _, manifest := range manifests {
		expected := manifest.Object.(*corev1.ConfigMap)
		_, err := spokeKubeClient.CoreV1().ConfigMaps(expected.Namespace).Get(context.Background(), expected.Name, metav1.GetOptions{})
		gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
	}
}

func AssertAppliedManifestWorkDeleted(name string, workClient workclientset.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() error {
		_, err := workClient.WorkV1().AppliedManifestWorks().Get(context.Background(), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("appliedwork %s still exists", name)
	}, eventuallyTimeout, eventuallyInterval).Should(gomega.Succeed())
}

// AssertFinalizerAdded check if finalizer is added
func AssertFinalizerAdded(namespace, name, expectedFinalizer string, workClient workclientset.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() error {
		work, err := workClient.WorkV1().ManifestWorks(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		for _, finalizer := range work.Finalizers {
			if finalizer == expectedFinalizer {
				return nil
			}
		}
		return fmt.Errorf("not found")
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

// AssertExistenceOfConfigMaps check if all manifests are applied
func AssertExistenceOfConfigMaps(manifests []workapiv1.Manifest, kubeClient kubernetes.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() error {
		for _, manifest := range manifests {
			expected := manifest.Object.(*corev1.ConfigMap)
			actual, err := kubeClient.CoreV1().ConfigMaps(expected.Namespace).Get(context.Background(), expected.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}

			if !reflect.DeepEqual(actual.Data, expected.Data) {
				return fmt.Errorf("configmap should be equal to %v, but got %v", expected.Data, actual.Data)
			}
		}

		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

// AssertNonexistenceOfConfigMaps check if configmap does not exist
func AssertNonexistenceOfConfigMaps(manifests []workapiv1.Manifest, kubeClient kubernetes.Interface,
	eventuallyTimeout, eventuallyInterval int) {
	gomega.Eventually(func() bool {
		for _, manifest := range manifests {
			expected := manifest.Object.(*corev1.ConfigMap)
			_, err := kubeClient.CoreV1().ConfigMaps(expected.Namespace).Get(
				context.Background(), expected.Name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}

		return false
	}, eventuallyTimeout, eventuallyInterval).Should(gomega.BeTrue())
}

// AssertExistenceOfResources check the existence of resource with GVR, namespace and name
func AssertExistenceOfResources(gvrs []schema.GroupVersionResource, namespaces, names []string,
	dynamicClient dynamic.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Expect(gvrs).To(gomega.HaveLen(len(namespaces)))
	gomega.Expect(gvrs).To(gomega.HaveLen(len(names)))

	gomega.Eventually(func() error {
		for i := range gvrs {
			_, err := GetResource(namespaces[i], names[i], gvrs[i], dynamicClient)
			if err != nil {
				return err
			}
		}

		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

// AssertNonexistenceOfResources check if resource with GVR, namespace and name does not exists
func AssertNonexistenceOfResources(gvrs []schema.GroupVersionResource, namespaces, names []string,
	dynamicClient dynamic.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Expect(gvrs).To(gomega.HaveLen(len(namespaces)))
	gomega.Expect(gvrs).To(gomega.HaveLen(len(names)))

	gomega.Eventually(func() bool {
		for i := range gvrs {
			_, err := GetResource(namespaces[i], names[i], gvrs[i], dynamicClient)
			if !apierrors.IsNotFound(err) {
				return false
			}
		}

		return true
	}, eventuallyTimeout, eventuallyInterval).Should(gomega.BeTrue())
}

// AssertAppliedResources check if applied resources in work status are updated correctly
func AssertAppliedResources(appliedManifestWorkName string, gvrs []schema.GroupVersionResource, namespaces, names []string,
	workClient workclientset.Interface, eventuallyTimeout, eventuallyInterval int) {
	gomega.Expect(gvrs).To(gomega.HaveLen(len(namespaces)))
	gomega.Expect(gvrs).To(gomega.HaveLen(len(names)))

	var appliedResources []workapiv1.AppliedManifestResourceMeta
	for i := range gvrs {
		appliedResources = append(appliedResources, workapiv1.AppliedManifestResourceMeta{
			ResourceIdentifier: workapiv1.ResourceIdentifier{
				Group:     gvrs[i].Group,
				Resource:  gvrs[i].Resource,
				Namespace: namespaces[i],
				Name:      names[i],
			},
			Version: gvrs[i].Version,
		})
	}

	sort.SliceStable(appliedResources, func(i, j int) bool {
		switch {
		case appliedResources[i].Group != appliedResources[j].Group:
			return appliedResources[i].Group < appliedResources[j].Group
		case appliedResources[i].Version != appliedResources[j].Version:
			return appliedResources[i].Version < appliedResources[j].Version
		case appliedResources[i].Resource != appliedResources[j].Resource:
			return appliedResources[i].Resource < appliedResources[j].Resource
		case appliedResources[i].Namespace != appliedResources[j].Namespace:
			return appliedResources[i].Namespace < appliedResources[j].Namespace
		default:
			return appliedResources[i].Name < appliedResources[j].Name
		}
	})

	gomega.Eventually(func() error {
		appliedManifestWork, err := workClient.WorkV1().AppliedManifestWorks().Get(
			context.Background(), appliedManifestWorkName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// remove uid from each AppliedManifestResourceMeta
		var actualAppliedResources []workapiv1.AppliedManifestResourceMeta
		for _, appliedResource := range appliedManifestWork.Status.AppliedResources {
			actualAppliedResources = append(actualAppliedResources, workapiv1.AppliedManifestResourceMeta{
				ResourceIdentifier: workapiv1.ResourceIdentifier{
					Group:     appliedResource.Group,
					Resource:  appliedResource.Resource,
					Namespace: appliedResource.Namespace,
					Name:      appliedResource.Name,
				},
				Version: appliedResource.Version,
			})
		}

		if !reflect.DeepEqual(actualAppliedResources, appliedResources) {
			return fmt.Errorf("applied resources not equal, expect: %v, actual: %v",
				appliedResources, actualAppliedResources)
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())
}

func HaveManifestCondition(conditions []workapiv1.ManifestCondition, expectedType string, expectedStatuses []metav1.ConditionStatus) bool {
	if expectedStatuses == nil {
		return true
	}
	if len(conditions) != len(expectedStatuses) {
		return false
	}

	for index, condition := range conditions {
		expectedStatus := expectedStatuses[index]
		if expectedStatus == "" {
			continue
		}

		if ok := meta.IsStatusConditionPresentAndEqual(condition.Conditions, expectedType, expectedStatus); !ok {
			return false
		}
	}

	return true
}
