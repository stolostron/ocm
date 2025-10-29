package proxyserver

import (
	"context"
	"time"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/pkg/errors"
	informercorev1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"open-cluster-management.io/ocm/pkg/clusterproxy/sscasigner"
	"open-cluster-management.io/sdk-go/pkg/certrotation"
)

type ProxyServerController struct {
	proxyServerAddress             string
	signer                         sscasigner.SelfSignedCASigner
	newCertRotatorFunc             func(namespace, name string, sans ...string) sscasigner.CertRotation
	proxyServerCertSecretNamespace string
	eventRecorder                  events.Recorder
}

const (
	proxyServerCertSecretName = "cluster-proxy-proxy-server-tls"
)

// NewProxyServerController creates a new proxy server controller
func NewProxyServerController(
	proxyServerAddress string,
	sscaSigner sscasigner.SelfSignedCASigner,
	hubKubeClient kubernetes.Interface,
	proxyServerCertSecretNamespace string,
	secretInformer informercorev1.SecretInformer,
	eventRecorder events.Recorder,
) factory.Controller {
	controller := &ProxyServerController{
		proxyServerAddress: proxyServerAddress,
		signer:             sscaSigner,
		newCertRotatorFunc: func(namespace, name string, sans ...string) sscasigner.CertRotation {
			return &certrotation.TargetRotation{
				Namespace: namespace,
				Name:      name,
				Validity:  time.Hour * 24 * 180,
				HostNames: sans,
				Lister:    secretInformer.Lister(),
				Client:    hubKubeClient.CoreV1(),
			}
		},
		proxyServerCertSecretNamespace: proxyServerCertSecretNamespace,
		eventRecorder:                  eventRecorder.WithComponentSuffix("cluster-proxy-proxy-server"),
	}

	return factory.New().
		WithSync(controller.sync).
		ResyncEvery(1*time.Hour).
		ToController("ProxyServerController", eventRecorder)
}

// sync handles the proxy server lifecycle
func (c *ProxyServerController) sync(ctx context.Context, controllerContext factory.SyncContext) error {
	// ensure the proxy server certificate rotation
	sans := []string{
		"localhost",
		"127.0.0.1",
		c.proxyServerAddress,
	}

	proxyServerRotator := c.newCertRotatorFunc(
		c.proxyServerCertSecretNamespace,
		proxyServerCertSecretName,
		sans...)

	if err := proxyServerRotator.EnsureTargetCertKeyPair(c.signer.CA(), c.signer.CA().Config.Certs); err != nil {
		return errors.Wrapf(err, "fails to rotate proxy server cert")
	}

	return nil
}
