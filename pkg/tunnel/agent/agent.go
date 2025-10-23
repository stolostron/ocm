package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

// Config holds all configuration for the Agent.
type Config struct {
	HubAddress     string
	ClusterName    string
	UDSSocketPath  string                 // Path for Unix Domain Socket, defaults to "/tmp/multiclustertunnel.sock"
	DialOptions    []grpc.DialOption      // Used to pass gRPC configurations such as TLS, KeepAlive, etc.
	BackoffFactory func() backoff.BackOff // Allows custom backoff strategy
}

// Agent connects to the tunnel server, establishes a grpc stream connection.
type Agent struct {
	config   *Config
	grpcConn *grpc.ClientConn
	lcm      packetConnManager
	proxy    *proxy
}

func New(ctx context.Context, config *Config,
	rp RequestProcessor, cp CertificateProvider, router Router) *Agent {
	// --- Initialize KeepAlive parameters ---
	// This is key to handling "zombie connections" (Case 2b)
	if config.DialOptions == nil {
		kacp := keepalive.ClientParameters{
			Time:                10 * time.Second, // Send a ping every 10 seconds
			Timeout:             5 * time.Second,  // If no pong is received within 5 seconds, consider the connection problematic
			PermitWithoutStream: true,             // Send pings even if there are no active streams
		}
		config.DialOptions = append(config.DialOptions, grpc.WithKeepaliveParams(kacp))
	}

	// --- Initialize exponential backoff strategy ---
	// This is key to handling "first connection failure", "normal reconnection", and "thundering herd effect" (Case 1a, 1b, 3b).
	// By default, NewExponentialBackOff is used, which provides a jittered exponential backoff.
	// The default configuration is as follows:
	// - InitialInterval: 500ms
	// - RandomizationFactor: 0.5
	// - Multiplier: 1.5
	// - MaxInterval: 60s
	// This means the first retry will occur after a random duration between 250ms and 750ms.
	// Subsequent retries will increase the interval by a factor of 1.5, with the same randomization,
	// up to a maximum interval of 60 seconds. This approach helps to prevent thundering herd scenarios
	// and provides a resilient reconnection mechanism.
	if config.BackoffFactory == nil {
		// return a default backoff factory
		config.BackoffFactory = func() backoff.BackOff {
			return backoff.NewExponentialBackOff()
		}
	}

	// Set default UDS socket path if not provided
	udsSocketPath := config.UDSSocketPath
	if udsSocketPath == "" {
		udsSocketPath = "/tmp/multiclustertunnel.sock"
	}

	return &Agent{
		config: config,
		lcm:    newPacketConnectionManagerWithSocketPath(ctx, udsSocketPath),
		proxy:  newProxy(rp, cp, router, udsSocketPath),
	}
}

func (c *Agent) Run(ctx context.Context) error {
	klog.InfoS("Agent starting")
	b := c.config.BackoffFactory()

	// Start serviceProxy in a separate goroutine
	serviceProxyErrCh := make(chan error, 1)
	go func() {
		klog.InfoS("Starting serviceProxy")
		serviceProxyErrCh <- c.proxy.Run(ctx)
	}()

	// Main agent loop for gRPC connection management
	agentErrCh := make(chan error, 1)
	go func() {
		defer close(agentErrCh)
		for {
			select {
			case <-ctx.Done():
				// graceful shutdown
				klog.InfoS("Context canceled, shutting down agent")

				// Close gRPC connection if it exists
				if c.grpcConn != nil {
					c.grpcConn.Close()
				}
				agentErrCh <- ctx.Err()
				return
			default:
				err := c.establishAndServe(ctx)
				if err != nil {
					// Check context before retrying
					if ctx.Err() != nil {
						agentErrCh <- ctx.Err()
						return
					}
					klog.ErrorS(err, "Session failed, retrying")
				}

				// Use a shorter retry interval that's also context-aware
				timer := time.NewTimer(b.NextBackOff())
				defer timer.Stop()

				select {
				case <-ctx.Done():
					agentErrCh <- ctx.Err()
					return
				case <-timer.C:
					// Continue to next iteration
				}
			}
		}
	}()

	// Wait for either serviceProxy or agent to fail/complete
	select {
	case err := <-serviceProxyErrCh:
		klog.ErrorS(err, "ServiceProxy failed")
		return fmt.Errorf("serviceProxy failed: %w", err)
	case err := <-agentErrCh:
		klog.InfoS("Agent main loop completed")
		return err
	}
}

func (c *Agent) establishAndServe(ctx context.Context) error {
	klog.InfoS("Attempting to connect to Hub", "address", c.config.HubAddress)

	// Establish gRPC connection
	conn, err := grpc.NewClient(c.config.HubAddress, c.config.DialOptions...)
	if err != nil {
		return fmt.Errorf("failed to dial hub: %w", err) // case 1a
	}
	defer conn.Close()
	c.grpcConn = conn

	klog.InfoS("Connection to Hub established")

	// Establish bidirectional grpc stream for tunnel
	tunnelClient := v1.NewTunnelServiceClient(conn)
	grpcStreamCtx := metadata.AppendToOutgoingContext(ctx, "cluster-name", c.config.ClusterName)
	grpcStream, err := tunnelClient.Tunnel(grpcStreamCtx)
	if err != nil {
		return fmt.Errorf("failed to create grpc stream for tunnel: %w", err)
	}

	return c.serve(ctx, grpcStream)
}

// serve manages a single active gRPC stream for tunnel.
// It blocks until the stream is terminated.
func (c *Agent) serve(ctx context.Context, stream v1.TunnelService_TunnelClient) error {
	klog.InfoS("GRPC stream started")
	defer klog.InfoS("GRPC stream ended")

	errCh := make(chan error, 3)

	// --- Goroutine 1: Handle packets from Hub ---
	go func() {
		errCh <- c.processIncoming(stream)
	}()

	// --- Goroutine 2: Handle packets to Hub ---
	go func() {
		errCh <- c.processOutgoing(stream)
	}()

	// --- Goroutine 3: Handle graceful shutdown ---
	go func() {
		<-ctx.Done()
		klog.InfoS("Context canceled, sending DRAIN signal to Hub")

		// Send DRAIN packet to Hub to indicate graceful shutdown
		drainPacket := &v1.Packet{
			ConnId: 0, // Use 0 for control messages
			Code:   v1.ControlCode_DRAIN,
		}

		// Try to send DRAIN packet with a timeout to avoid blocking indefinitely
		done := make(chan error, 1)
		go func() {
			done <- stream.Send(drainPacket)
		}()

		select {
		case err := <-done:
			if err != nil {
				klog.ErrorS(err, "Failed to send DRAIN packet to Hub")
			} else {
				klog.InfoS("DRAIN packet sent to Hub successfully")
			}
		case <-time.After(100 * time.Millisecond):
			klog.InfoS("Timeout sending DRAIN packet to Hub")
		}

		errCh <- ctx.Err()
	}()

	// Wait for any goroutine to exit (i.e., stream error or closure)
	err := <-errCh
	return err
}

// processIncoming continuously receives Packets from the Hub and dispatches them
func (c *Agent) processIncoming(grpcStream v1.TunnelService_TunnelClient) error {
	for {
		packet, err := grpcStream.Recv()
		if err != nil {
			// e.g., io.EOF, or connection reset by peer
			return err
		}

		go func() {
			if err := c.lcm.Dispatch(packet); err != nil {
				klog.ErrorS(err, "Failed to dispatch packet", "conn_id", packet.ConnId, "code", packet.Code)

				// Send error response back to Hub for this specific connection
				errorPacket := &v1.Packet{
					ConnId:       packet.ConnId,
					Code:         v1.ControlCode_ERROR,
					ErrorMessage: err.Error(),
				}

				// Best effort to send error response - don't fail the entire stream if this fails
				if sendErr := grpcStream.Send(errorPacket); sendErr != nil {
					klog.ErrorS(sendErr, "Failed to send error response to Hub", "conn_id", packet.ConnId)
				}
			}
		}()
	}
}

// processOutgoing continuously sends all Packets generated by local services to the Hub
func (c *Agent) processOutgoing(grpcStream v1.TunnelService_TunnelClient) error {
	// c.connectionManager.OutgoingChan() returns a channel aggregating all Packets to be sent from local services
	for packet := range c.lcm.OutgoingChan() {
		if err := grpcStream.Send(packet); err != nil {
			return err
		}
	}
	return errors.New("outgoing channel closed")
}
