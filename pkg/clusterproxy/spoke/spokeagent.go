package spoke

import (
	"context"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"open-cluster-management.io/ocm/pkg/common/options"
)

type ClusterProxyAgentConfig struct {
	agentOptions        *options.AgentOptions
	clusterproxyOptions *ClusterProxyAgentOptions
}

func NewClusterProxyConfig(commonOpts *options.AgentOptions, opts *ClusterProxyAgentOptions) *ClusterProxyAgentConfig {
	return &ClusterProxyAgentConfig{
		agentOptions:        commonOpts,
		clusterproxyOptions: opts,
	}
}

func (c *ClusterProxyAgentConfig) RunClusterProxyAgent(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	// TODO: first, implement the send csr, hub approve, and spoke fetch csr to get certification data logic.
	<-ctx.Done()
	return nil
}
