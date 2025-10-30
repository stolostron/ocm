package spoke

import "github.com/spf13/pflag"

// ClusterProxyAgentOptions contains the options for cluster-proxy agent
type ClusterProxyAgentOptions struct {
	// certificates for secure connection to hub
	HubKubeConfig string

	// proxy
	ProxyServerAddress string
}

func NewClusterProxyAgentOptions() *ClusterProxyAgentOptions {
	return &ClusterProxyAgentOptions{
		HubKubeConfig: "/spoke/hub-kubeconfig/kubeconfig",
	}
}

func (o *ClusterProxyAgentOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.HubKubeConfig, "cluster-proxy-hub-kubeconfig", o.HubKubeConfig,
		"The path of the kubeconfig file for cluster-proxy to connect to hub.")

	fs.StringVar(&o.ProxyServerAddress, "cluster-proxy-server-address", o.ProxyServerAddress,
		"The address of the cluster-proxy server.")
}
