package aws_irsa

import (
	"fmt"

	"github.com/openshift/library-go/pkg/controller/factory"
	"k8s.io/apimachinery/pkg/api/meta"

	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	hubclusterclientset "open-cluster-management.io/api/client/cluster/clientset/versioned"
	managedclusterinformers "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster"

	"open-cluster-management.io/ocm/pkg/registration/register"
)

// AWSOption includes options that is used to monitor ManagedClusters
type AWSOption struct {
	EventFilterFunc factory.EventFilterFunc
	AWSIRSAControl  AWSIRSAControl
}

func NewAWSOption(
	secretOption register.SecretOption,
	hubManagedClusterInformer managedclusterinformers.Interface,
	hubClusterClientSet hubclusterclientset.Interface) (*AWSOption, error) {
	awsIrsaControl, err := NewAWSIRSAControl(hubManagedClusterInformer, hubClusterClientSet)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS IRSA control: %w", err)
	}
	if err != nil {
		return nil, err
	}
	return &AWSOption{
		EventFilterFunc: func(obj interface{}) bool {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				return false
			}
			labels := accessor.GetLabels()

			// should not contain addon key
			_, ok := labels[addonv1alpha1.AddonLabelKey]
			if ok {
				return false
			}

			// only enqueue csr whose name starts with the cluster name
			return accessor.GetName() == secretOption.ClusterName
		},
		AWSIRSAControl: awsIrsaControl,
	}, nil
}
