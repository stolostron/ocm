package hub

import (
	"context"
	"time"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/pflag"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"open-cluster-management.io/ocm/pkg/clusterproxy/hub/proxyserver"
	"open-cluster-management.io/ocm/pkg/clusterproxy/sscasigner"
)

type ClusterProxyManagerOptions struct {
	ProxyServerAddress string
}

func NewClusterProxyManagerOptions() *ClusterProxyManagerOptions {
	return &ClusterProxyManagerOptions{}
}

func (o *ClusterProxyManagerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ProxyServerAddress, "proxy-server-address", o.ProxyServerAddress,
		"The address of the cluster proxy server")
}

func (o *ClusterProxyManagerOptions) RunClusterProxyManager(
	ctx context.Context, controllerContext *controllercmd.ControllerContext,
) error {
	hubKubeClient, err := kubernetes.NewForConfig(controllerContext.KubeConfig)
	if err != nil {
		return err
	}

	// loading self-signed CA signer
	clusterproxySigner, err := sscasigner.NewSelfSignedCASignerFromSecretOrGenerate(
		hubKubeClient,
		controllerContext.OperatorNamespace)
	if err != nil {
		return err
	}

	kubeInformerFactory := informers.NewSharedInformerFactory(hubKubeClient, 30*time.Minute)

	proxyServerController := proxyserver.NewProxyServerController(
		o.ProxyServerAddress,
		clusterproxySigner,
		hubKubeClient,
		controllerContext.OperatorNamespace,
		kubeInformerFactory.Core().V1().Secrets(),
		controllerContext.EventRecorder,
	)

	go kubeInformerFactory.Start(ctx.Done())
	go proxyServerController.Run(ctx, 1)

	<-ctx.Done()
	return nil
}
