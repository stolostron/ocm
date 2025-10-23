package grpc

import (
	"context"
	"crypto/tls"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	tunnelpbv1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
	tunnelserver "open-cluster-management.io/ocm/pkg/tunnel/server"
	addonce "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/addon"
	clusterce "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster"
	csrce "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr"
	eventce "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/event"
	leasece "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/lease"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/work/payload"
	pbv1 "open-cluster-management.io/sdk-go/pkg/cloudevents/generic/options/grpc/protobuf/v1"
	cloudeventsgrpc "open-cluster-management.io/sdk-go/pkg/cloudevents/server/grpc"
	grpcauthz "open-cluster-management.io/sdk-go/pkg/cloudevents/server/grpc/authz/kube"
	cemetrics "open-cluster-management.io/sdk-go/pkg/cloudevents/server/grpc/metrics"
	sdkgrpc "open-cluster-management.io/sdk-go/pkg/server/grpc"
	grpcauthn "open-cluster-management.io/sdk-go/pkg/server/grpc/authn"

	"open-cluster-management.io/ocm/pkg/server/services/addon"
	"open-cluster-management.io/ocm/pkg/server/services/cluster"
	"open-cluster-management.io/ocm/pkg/server/services/csr"
	"open-cluster-management.io/ocm/pkg/server/services/event"
	"open-cluster-management.io/ocm/pkg/server/services/lease"
	"open-cluster-management.io/ocm/pkg/server/services/work"
)

type GRPCServerOptions struct {
	GRPCServerConfig string

	EnableClusterProxy       bool
	TunnelUserServerCertFile string
	TunnelUserServerKeyFile  string
}

func NewGRPCServerOptions() *GRPCServerOptions {
	return &GRPCServerOptions{
		EnableClusterProxy: false,
	}
}

func (o *GRPCServerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.GRPCServerConfig, "server-config", o.GRPCServerConfig, "Location of the server configuration file.")

	fs.BoolVar(&o.EnableClusterProxy, "cluster-proxy-enable", o.EnableClusterProxy, "Enable cluster proxy.")
	fs.StringVar(&o.TunnelUserServerCertFile, "tunnel-user-server-cert-file",
		o.TunnelUserServerCertFile, "Path to the tunnel user server certificate file.")
	fs.StringVar(&o.TunnelUserServerKeyFile, "tunnel-user-server-key-file",
		o.TunnelUserServerKeyFile, "Path to the tunnel user server key file.")
}

func (o *GRPCServerOptions) Run(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	serverOptions, err := sdkgrpc.LoadGRPCServerOptions(o.GRPCServerConfig)
	if err != nil {
		return err
	}

	clients, err := NewClients(controllerContext)
	if err != nil {
		return err
	}

	ts, err := tunnelserver.New(tunnelserver.NewClusterNameParserImplt())
	if err != nil {
		return err
	}

	// start tunnel user-server if cluster-proxy is enabled
	if o.EnableClusterProxy {
		cert, err := tls.LoadX509KeyPair(o.TunnelUserServerCertFile, o.TunnelUserServerKeyFile)
		if err != nil {
			return err
		}
		go tunnelserver.RunTunnelUserServer(ctx, ts, ":9092", &tls.Config{
			MinVersion: tls.VersionTLS13,
			Certificates: []tls.Certificate{
				cert,
			},
		})
	}

	// start clients
	go clients.Run(ctx)

	// initlize grpc broker and register services
	grpcEventServer := cloudeventsgrpc.NewGRPCBroker()
	grpcEventServer.RegisterService(clusterce.ManagedClusterEventDataType,
		cluster.NewClusterService(clients.ClusterClient, clients.ClusterInformers.Cluster().V1().ManagedClusters()))
	grpcEventServer.RegisterService(csrce.CSREventDataType,
		csr.NewCSRService(clients.KubeClient, clients.KubeInformers.Certificates().V1().CertificateSigningRequests()))
	grpcEventServer.RegisterService(addonce.ManagedClusterAddOnEventDataType,
		addon.NewAddonService(clients.AddOnClient, clients.AddOnInformers.Addon().V1alpha1().ManagedClusterAddOns()))
	grpcEventServer.RegisterService(eventce.EventEventDataType,
		event.NewEventService(clients.KubeClient))
	grpcEventServer.RegisterService(leasece.LeaseEventDataType,
		lease.NewLeaseService(clients.KubeClient, clients.KubeInformers.Coordination().V1().Leases()))
	grpcEventServer.RegisterService(payload.ManifestBundleEventDataType,
		work.NewWorkService(clients.WorkClient, clients.WorkInformers.Work().V1().ManifestWorks()))

	// initlize and run grpc server
	authorizer := grpcauthz.NewSARAuthorizer(clients.KubeClient)

	grpcServer := sdkgrpc.NewGRPCServer(serverOptions).
		WithAuthenticator(grpcauthn.NewTokenAuthenticator(clients.KubeClient)).
		WithAuthenticator(grpcauthn.NewMtlsAuthenticator()).
		WithUnaryAuthorizer(authorizer).
		WithStreamAuthorizer(authorizer).
		WithRegisterFunc(func(s *grpc.Server) {
			pbv1.RegisterCloudEventServiceServer(s, grpcEventServer)
		}).
		WithExtraMetrics(cemetrics.CloudEventsGRPCMetrics()...)

	// register tunnel service if cluster-proxy is enabled
	if o.EnableClusterProxy {
		grpcServer = grpcServer.WithRegisterFunc(func(s *grpc.Server) {
			tunnelpbv1.RegisterTunnelServiceServer(s, ts)
		})
	}

	return grpcServer.Run(ctx)
}
