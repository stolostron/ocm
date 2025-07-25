/*
 * Copyright 2022 Contributors to the Open Cluster Management project
 */

package klusterletcontroller

import (
	"context"
	"fmt"
	"strings"

	"github.com/openshift/library-go/pkg/assets"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	operatorapiv1 "open-cluster-management.io/api/operator/v1"

	"open-cluster-management.io/ocm/manifests"
	"open-cluster-management.io/ocm/pkg/operator/helpers"
)

// runtimeReconcile ensure all runtime of klusterlet is applied
type runtimeReconcile struct {
	managedClusterClients *managedClusterClients
	kubeClient            kubernetes.Interface
	recorder              events.Recorder
	cache                 resourceapply.ResourceCache
	enableSyncLabels      bool
}

func (r *runtimeReconcile) reconcile(ctx context.Context, klusterlet *operatorapiv1.Klusterlet,
	config klusterletConfig) (*operatorapiv1.Klusterlet, reconcileState, error) {
	if helpers.IsSingleton(config.InstallMode) {
		return r.installSingletonAgent(ctx, klusterlet, config)
	}

	return r.installAgent(ctx, klusterlet, config)
}

func (r *runtimeReconcile) installAgent(ctx context.Context, klusterlet *operatorapiv1.Klusterlet,
	runtimeConfig klusterletConfig) (*operatorapiv1.Klusterlet, reconcileState, error) {
	if helpers.IsHosted(runtimeConfig.InstallMode) {
		// Create managed config secret for registration and work.
		if err := r.createManagedClusterKubeconfig(ctx, klusterlet, runtimeConfig.KlusterletNamespace, runtimeConfig.AgentNamespace,
			runtimeConfig.RegistrationServiceAccount, runtimeConfig.ExternalManagedKubeConfigRegistrationSecret,
			r.recorder); err != nil {
			return klusterlet, reconcileStop, err
		}
		if err := r.createManagedClusterKubeconfig(ctx, klusterlet, runtimeConfig.KlusterletNamespace, runtimeConfig.AgentNamespace,
			runtimeConfig.WorkServiceAccount, runtimeConfig.ExternalManagedKubeConfigWorkSecret,
			r.recorder); err != nil {
			return klusterlet, reconcileStop, err
		}
	}
	// Deploy registration agent
	_, generationStatus, err := helpers.ApplyDeployment(
		ctx,
		r.kubeClient,
		klusterlet.Status.Generations,
		klusterlet.Spec.NodePlacement,
		func(name string) ([]byte, error) {
			template, err := manifests.KlusterletManifestFiles.ReadFile(name)
			if err != nil {
				return nil, err
			}
			objData := assets.MustCreateAssetFromTemplate(name, template, runtimeConfig).Data
			helpers.SetRelatedResourcesStatusesWithObj(&klusterlet.Status.RelatedResources, objData)
			return objData, nil
		},
		r.recorder,
		"klusterlet/management/klusterlet-registration-deployment.yaml")

	if err != nil {
		// TODO update condition
		return klusterlet, reconcileStop, err
	}

	helpers.SetGenerationStatuses(&klusterlet.Status.Generations, generationStatus)

	// If cluster name is empty, read cluster name from hub config secret.
	// registration-agent generated the cluster name and set it into hub config secret.
	workConfig := runtimeConfig
	if workConfig.ClusterName == "" {
		workConfig.ClusterName, err = r.getClusterNameFromHubKubeConfigSecret(ctx, runtimeConfig.AgentNamespace, klusterlet)
		if err != nil {
			return klusterlet, reconcileStop, err
		}
	}

	// Deploy work agent.
	// * work agent is scaled to 0 only when degrade is true with the reason is HubKubeConfigSecretMissing.
	//   It is to ensure a fast startup of work agent when the klusterlet is bootstrapped at the first time.
	// * The work agent should not be scaled to 0 in degraded condition with other reasons,
	//   because we still need work agent running even though the hub kubconfig is missing some certain permission.
	//   It can ensure work agent to clean up the resources defined in manifestworks when cluster is detaching from the hub.
	hubConnectionDegradedCondition := meta.FindStatusCondition(klusterlet.Status.Conditions, operatorapiv1.ConditionHubConnectionDegraded)
	if hubConnectionDegradedCondition == nil {
		workConfig.Replica = 0
	} else if hubConnectionDegradedCondition.Status == metav1.ConditionTrue &&
		strings.Contains(hubConnectionDegradedCondition.Reason, operatorapiv1.ReasonHubKubeConfigSecretMissing) {
		workConfig.Replica = 0
	}

	// Deploy work agent
	_, generationStatus, err = helpers.ApplyDeployment(
		ctx,
		r.kubeClient,
		klusterlet.Status.Generations,
		klusterlet.Spec.NodePlacement,
		func(name string) ([]byte, error) {
			template, err := manifests.KlusterletManifestFiles.ReadFile(name)
			if err != nil {
				return nil, err
			}
			objData := assets.MustCreateAssetFromTemplate(name, template, workConfig).Data
			helpers.SetRelatedResourcesStatusesWithObj(&klusterlet.Status.RelatedResources, objData)
			return objData, nil
		},
		r.recorder,
		"klusterlet/management/klusterlet-work-deployment.yaml")

	if err != nil {
		// TODO update condition
		return klusterlet, reconcileStop, err
	}

	// clean singleton agent if there is any
	deployments := []string{fmt.Sprintf("%s-agent", runtimeConfig.KlusterletName)}
	for _, deployment := range deployments {
		err := r.kubeClient.AppsV1().Deployments(runtimeConfig.AgentNamespace).Delete(ctx, deployment, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return klusterlet, reconcileStop, err
		}
	}

	helpers.SetGenerationStatuses(&klusterlet.Status.Generations, generationStatus)

	// TODO check progressing condition

	return klusterlet, reconcileContinue, nil
}

func (r *runtimeReconcile) installSingletonAgent(ctx context.Context, klusterlet *operatorapiv1.Klusterlet,
	config klusterletConfig) (*operatorapiv1.Klusterlet, reconcileState, error) {
	if helpers.IsHosted(config.InstallMode) {
		// Create managed config secret for agent. In singletonHosted mode, service account for registration/work is actually
		// the same one, and we just pick one of them to build the external kubeconfig.
		if err := r.createManagedClusterKubeconfig(ctx, klusterlet, config.KlusterletNamespace, config.AgentNamespace,
			config.WorkServiceAccount, config.ExternalManagedKubeConfigAgentSecret,
			r.recorder); err != nil {
			return klusterlet, reconcileStop, err
		}
	}
	// Deploy singleton agent
	_, generationStatus, err := helpers.ApplyDeployment(
		ctx,
		r.kubeClient,
		klusterlet.Status.Generations,
		klusterlet.Spec.NodePlacement,
		func(name string) ([]byte, error) {
			template, err := manifests.KlusterletManifestFiles.ReadFile(name)
			if err != nil {
				return nil, err
			}
			objData := assets.MustCreateAssetFromTemplate(name, template, config).Data
			helpers.SetRelatedResourcesStatusesWithObj(&klusterlet.Status.RelatedResources, objData)
			return objData, nil
		},
		r.recorder,
		"klusterlet/management/klusterlet-agent-deployment.yaml")

	if err != nil {
		// TODO update condition
		return klusterlet, reconcileStop, err
	}

	// clean registration/work if there is any
	deployments := []string{
		fmt.Sprintf("%s-registration-agent", config.KlusterletName),
		fmt.Sprintf("%s-work-agent", config.KlusterletName),
	}
	for _, deployment := range deployments {
		err := r.kubeClient.AppsV1().Deployments(config.AgentNamespace).Delete(ctx, deployment, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return klusterlet, reconcileStop, err
		}
	}

	helpers.SetGenerationStatuses(&klusterlet.Status.Generations, generationStatus)
	return klusterlet, reconcileContinue, nil
}

func (r *runtimeReconcile) createManagedClusterKubeconfig(
	ctx context.Context,
	klusterlet *operatorapiv1.Klusterlet,
	klusterletNamespace, agentNamespace, saName, secretName string,
	recorder events.Recorder) error {
	labels := helpers.GetKlusterletAgentLabels(klusterlet, r.enableSyncLabels)

	tokenGetter := helpers.SATokenGetter(ctx, saName, klusterletNamespace, r.managedClusterClients.kubeClient)
	err := helpers.SyncKubeConfigSecret(ctx, secretName, agentNamespace, "/spoke/config/kubeconfig",
		r.managedClusterClients.kubeconfig, r.kubeClient.CoreV1(), tokenGetter, recorder, labels)
	if err != nil {
		meta.SetStatusCondition(&klusterlet.Status.Conditions, metav1.Condition{
			Type: operatorapiv1.ConditionKlusterletApplied, Status: metav1.ConditionFalse, Reason: operatorapiv1.ReasonKlusterletApplyFailed,
			Message: fmt.Sprintf("Failed to create managed kubeconfig secret %s with error %v", secretName, err),
		})
	}
	return err
}

func (r *runtimeReconcile) getClusterNameFromHubKubeConfigSecret(ctx context.Context, namespace string, klusterlet *operatorapiv1.Klusterlet) (string, error) {
	hubSecret, err := r.kubeClient.CoreV1().Secrets(namespace).Get(ctx, helpers.HubKubeConfig, metav1.GetOptions{})
	if err != nil {
		meta.SetStatusCondition(&klusterlet.Status.Conditions, metav1.Condition{
			Type: operatorapiv1.ConditionKlusterletApplied, Status: metav1.ConditionFalse, Reason: operatorapiv1.ReasonKlusterletApplyFailed,
			Message: fmt.Sprintf("Failed to get cluster name from hub kubeconfig secret with error %v", err),
		})
		return "", err
	}

	clusterName := hubSecret.Data["cluster-name"]
	if len(clusterName) == 0 {
		meta.SetStatusCondition(&klusterlet.Status.Conditions, metav1.Condition{
			Type: operatorapiv1.ConditionKlusterletApplied, Status: metav1.ConditionFalse, Reason: operatorapiv1.ReasonKlusterletApplyFailed,
			Message: fmt.Sprintf("Failed to get cluster name from hub kubeconfig secret with error: %v", fmt.Errorf("the cluster name in the secret is empty")),
		})
		return "", fmt.Errorf("the cluster name in the secret is empty")
	}
	return string(clusterName), nil
}

func (r *runtimeReconcile) clean(ctx context.Context, klusterlet *operatorapiv1.Klusterlet,
	config klusterletConfig) (*operatorapiv1.Klusterlet, reconcileState, error) {
	deployments := []string{
		fmt.Sprintf("%s-registration-agent", config.KlusterletName),
		fmt.Sprintf("%s-work-agent", config.KlusterletName),
	}
	if helpers.IsSingleton(klusterlet.Spec.DeployOption.Mode) {
		deployments = []string{fmt.Sprintf("%s-agent", config.KlusterletName)}
	}
	for _, deployment := range deployments {
		err := r.kubeClient.AppsV1().Deployments(config.AgentNamespace).Delete(ctx, deployment, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return klusterlet, reconcileStop, err
		}
		r.recorder.Eventf("DeploymentDeleted", "deployment %s is deleted", deployment)
	}

	return klusterlet, reconcileContinue, nil
}
