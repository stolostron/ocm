package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

// TargetAddress Usage Strategy:
//
// The TargetAddress field in v1.Packet is used strategically to optimize performance:
//
// 1. CONNECTION ESTABLISHMENT PHASE:
//    - TargetAddress MUST be set in packets that establish new connections
//    - This includes the initial empty packet and the first HTTP request packet
//    - The agent uses this information to determine which target service to connect to
//
// 2. DATA FORWARDING PHASE:
//    - TargetAddress is NOT set in subsequent data forwarding packets
//    - Once the connection is established, the agent knows where to forward data
//    - Omitting TargetAddress reduces packet size and improves performance
//
// This design ensures correct routing while minimizing unnecessary data transmission.

// Config holds all configuration for the Hub Server
type Config struct {
	// Address to listen on for gRPC connections from agents
	GRPCListenAddress string
	// Address to listen on for HTTP connections from users
	HTTPListenAddress string
	// ServerOptions for gRPC server configuration
	ServerOptions []grpc.ServerOption
	// KeepAlive settings for server
	KeepAliveParams *keepalive.ServerParameters
	// TLS configuration for gRPC server (optional)
	GRPCTLSConfig *tls.Config
	// TLS configuration for HTTP server (optional)
	HTTPTLSConfig *tls.Config
}

// Server implements the hub-side tunnel server with both gRPC and HTTP servers
type Server struct {
	config        *Config
	grpcServer    *grpc.Server
	httpServer    *http.Server
	tunnelManager *TunnelManager
	grpcListener  net.Listener
	httpListener  net.Listener

	// Server state
	mu      sync.RWMutex
	running bool
	ready   bool

	// Embed the unimplemented server to satisfy the interface
	v1.UnimplementedTunnelServiceServer

	ClusterNameParser
}

// New creates a new Hub server instance
func New(config *Config, parser ClusterNameParser) (*Server, error) {
	if config == nil {
		config = DefaultConfig()
	}

	// Set default keepalive parameters if not provided
	if config.KeepAliveParams == nil {
		config.KeepAliveParams = &keepalive.ServerParameters{
			Time:    60 * time.Second, // Send keepalive every 60 seconds
			Timeout: 5 * time.Second,  // Wait 5 seconds for keepalive response
		}
	}

	// Add keepalive to server options
	serverOpts := append(config.ServerOptions, grpc.KeepaliveParams(*config.KeepAliveParams))

	// Add TLS credentials if TLS config is provided
	if config.GRPCTLSConfig != nil {
		creds := credentials.NewTLS(config.GRPCTLSConfig)
		serverOpts = append(serverOpts, grpc.Creds(creds))
		klog.InfoS("TLS enabled for gRPC server")
	} else {
		klog.InfoS("TLS not configured for gRPC server - using insecure connection")
	}

	// Create gRPC server
	grpcServer := grpc.NewServer(serverOpts...)

	// Create tunnel manager
	tunnelManager := NewTunnelManager()

	server := &Server{
		config:        config,
		grpcServer:    grpcServer,
		tunnelManager: tunnelManager,
	}

	// Create HTTP server
	handler := &httpHandler{
		tunnelManager: tunnelManager,
		parser:        parser,
	}
	// Wrap the handler to handle health checks
	wrappedHandler := &healthCheckHandler{
		handler: handler,
	}
	httpServer := &http.Server{
		Addr:    config.HTTPListenAddress,
		Handler: wrappedHandler,
		// Disable automatic HTTP/2 upgrade to support SPDY protocol used by kubectl exec
		// HTTP/2 cannot upgrade to SPDY, so we need to prevent automatic HTTP/2 negotiation
		// This allows clients like kubectl to use SPDY for exec/port-forward operations
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	// Add TLS configuration to HTTP server if provided
	if config.HTTPTLSConfig != nil {
		httpServer.TLSConfig = config.HTTPTLSConfig.Clone()
		klog.InfoS("TLS enabled for HTTP server")
	} else {
		klog.InfoS("TLS not configured for HTTP server - using insecure connection")
	}

	server.httpServer = httpServer

	// Register the tunnel service
	v1.RegisterTunnelServiceServer(grpcServer, server)

	return server, nil
}

// DefaultConfig returns a default configuration for the hub server
func DefaultConfig() *Config {
	return &Config{
		GRPCListenAddress: ":9091", // gRPC server for agents
		HTTPListenAddress: ":9092", // HTTP server for users
		KeepAliveParams: &keepalive.ServerParameters{
			MaxConnectionIdle:     15 * time.Second,
			MaxConnectionAge:      30 * time.Second,
			MaxConnectionAgeGrace: 5 * time.Second,
			Time:                  5 * time.Second,
			Timeout:               1 * time.Second,
		},
	}
}

// Run starts the hub server and blocks until the context is canceled
func (s *Server) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server is already running")
	}
	s.running = true
	s.mu.Unlock()

	klog.InfoS("Starting hub server", "grpc_address", s.config.GRPCListenAddress, "http_address", s.config.HTTPListenAddress)

	// Create gRPC listener
	grpcListener, err := net.Listen("tcp", s.config.GRPCListenAddress)
	if err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on gRPC address %s: %w", s.config.GRPCListenAddress, err)
	}
	s.grpcListener = grpcListener

	// Create HTTP listener if HTTP server is configured
	if s.httpServer != nil {
		httpListener, err := net.Listen("tcp", s.config.HTTPListenAddress)
		if err != nil {
			grpcListener.Close()
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return fmt.Errorf("failed to listen on HTTP address %s: %w", s.config.HTTPListenAddress, err)
		}
		s.httpListener = httpListener
	}

	// Mark server as ready
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()

	klog.InfoS("Hub server is ready", "grpc_address", grpcListener.Addr().String())
	if s.httpListener != nil {
		if s.config.HTTPTLSConfig != nil {
			klog.InfoS("HTTPS server is ready", "https_address", s.httpListener.Addr().String())
		} else {
			klog.InfoS("HTTP server is ready", "http_address", s.httpListener.Addr().String())
		}
	}

	// Start both servers in goroutines
	errCh := make(chan error, 2)

	// Start gRPC server
	go func() {
		klog.InfoS("Starting gRPC server", "address", grpcListener.Addr().String())
		errCh <- s.grpcServer.Serve(grpcListener)
	}()

	// Start HTTP server if configured
	if s.httpServer != nil && s.httpListener != nil {
		go func() {
			if s.config.HTTPTLSConfig != nil {
				klog.InfoS("Starting HTTPS server", "address", s.httpListener.Addr().String())
				errCh <- s.httpServer.ServeTLS(s.httpListener, "", "")
			} else {
				klog.InfoS("Starting HTTP server", "address", s.httpListener.Addr().String())
				errCh <- s.httpServer.Serve(s.httpListener)
			}
		}()
	}

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		klog.InfoS("Context canceled, shutting down hub server")
		return s.shutdown()
	case err := <-errCh:
		s.mu.Lock()
		s.running = false
		s.ready = false
		s.mu.Unlock()
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	}
}

// Shutdown gracefully shuts down the hub server
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	return s.shutdown()
}

// shutdown performs the actual shutdown logic
func (s *Server) shutdown() error {
	s.mu.Lock()
	s.running = false
	s.ready = false
	s.mu.Unlock()

	klog.InfoS("Shutting down hub server")

	// Stop HTTP server first
	if s.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			klog.ErrorS(err, "Failed to shutdown HTTP server gracefully")
		}
	}

	// Stop gRPC server gracefully with timeout
	done := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(done)
	}()

	// Wait for graceful stop or timeout
	select {
	case <-done:
		// Graceful stop completed
	case <-time.After(2 * time.Second):
		// Force stop if graceful stop takes too long
		klog.InfoS("Forcing gRPC server stop due to timeout")
		s.grpcServer.Stop()
	}

	// Close listeners
	if s.grpcListener != nil {
		s.grpcListener.Close()
	}
	if s.httpListener != nil {
		s.httpListener.Close()
	}

	// Close tunnel manager
	if s.tunnelManager != nil {
		s.tunnelManager.Close()
	}

	klog.InfoS("Hub server shutdown complete")
	return nil
}

// Ready returns true if the server is ready to accept connections
func (s *Server) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// GRPCAddress returns the actual gRPC server address
func (s *Server) GRPCAddress() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.grpcListener != nil {
		return s.grpcListener.Addr().String()
	}
	return s.config.GRPCListenAddress
}

// HTTPAddress returns the actual HTTP server address
func (s *Server) HTTPAddress() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.httpListener != nil {
		return s.httpListener.Addr().String()
	}
	return s.config.HTTPListenAddress
}

// GetTunnel returns the tunnel for a specific cluster
func (s *Server) GetTunnel(clusterName string) *Tunnel {
	if s.tunnelManager == nil {
		return nil
	}
	return s.tunnelManager.GetTunnel(clusterName)
}

// Tunnel implements the TunnelService gRPC interface
// This is called when an agent establishes a tunnel
func (s *Server) Tunnel(stream v1.TunnelService_TunnelServer) error {
	// Extract cluster information from metadata
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return fmt.Errorf("no metadata found in request")
	}

	clusterNames := md.Get("cluster-name")
	if len(clusterNames) == 0 {
		return fmt.Errorf("cluster-name not found in metadata")
	}
	clusterName := clusterNames[0]

	klog.InfoS("New tunnel", "cluster", clusterName)

	// Create a new tunnel
	conn, err := s.tunnelManager.NewTunnel(stream.Context(), clusterName, stream)
	if err != nil {
		klog.ErrorS(err, "Failed to create tunnel", "cluster", clusterName)
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	// Handle the tunnel (this blocks until the tunnel is closed)
	err = conn.Serve()

	// Clean up when tunnel ends
	s.tunnelManager.RemoveTunnel(clusterName, conn.ID())

	if err != nil {
		klog.ErrorS(err, "Tunnel ended with error", "cluster", clusterName)
	} else {
		klog.InfoS("Tunnel ended", "cluster", clusterName)
	}

	return err
}

// httpHandler implements http.Handler and handles HTTP requests using Router
type httpHandler struct {
	tunnelManager *TunnelManager
	parser        ClusterNameParser
}

// healthCheckHandler wraps the httpHandler to provide health check endpoint
type healthCheckHandler struct {
	handler *httpHandler
}

// ServeHTTP handles HTTP requests, including health checks
func (h *healthCheckHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle health check endpoint
	if r.URL.Path == "/health" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	// Delegate all other requests to the main handler
	h.handler.ServeHTTP(w, r)
}

// ServeHTTP handles HTTP requests and routes them to appropriate clusters using HTTP CONNECT tunneling
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	klog.V(4).InfoS("Received HTTP request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

	// Parse cluster name using the configured parser
	clusterName, err := h.parser.ParseClusterName(r)
	if err != nil {
		klog.ErrorS(err, "Failed to parse cluster name and target address from request", "path", r.URL.Path)
		http.Error(w, fmt.Sprintf("Failed to parse cluster name and target address from request, path:%s", r.URL.Path), http.StatusBadRequest)
		return
	}

	klog.V(4).InfoS("Routing request to cluster", "cluster", clusterName, "path", r.URL.Path)

	// Create a new packet connection to the target cluster
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Get tunnel for the cluster
	tun := h.tunnelManager.GetTunnel(clusterName)
	if tun == nil {
		klog.ErrorS(nil, "No tunnel found for cluster", "cluster", clusterName)
		http.Error(w, fmt.Sprintf("Cluster %s not available", clusterName), http.StatusServiceUnavailable)
		return
	}

	// Create new packet connection
	pc, err := tun.NewPacketConn(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to create packet connection to cluster", "cluster", clusterName)
		http.Error(w, fmt.Sprintf("Cluster %s not available: %v", clusterName, err), http.StatusServiceUnavailable)
		return
	}
	defer pc.Close(nil)

	// Hijack the HTTP connection to create a transparent tunnel
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		klog.ErrorS(nil, "HTTP hijacking not supported")
		http.Error(w, "HTTP tunneling not supported", http.StatusInternalServerError)
		return
	}

	// Send initial packet to establish connection on agent side
	// NOTE: TargetAddress is required here because this is the first packet that tells
	// the agent which target service to connect to. This establishes the connection.
	initialPacket := &v1.Packet{
		ConnId: pc.ID(),
		Code:   v1.ControlCode_DATA,
		Data:   []byte{}, // Empty data to trigger connection creation
	}

	if err := pc.Send(initialPacket); err != nil {
		klog.ErrorS(err, "Failed to send initial packet to agent", "cluster", clusterName)
		http.Error(w, "Failed to establish tunnel", http.StatusBadGateway)
		return
	}

	// Send the original HTTP request to establish the connection and start communication
	if err := h.sendInitialHTTPRequest(pc, r); err != nil {
		klog.ErrorS(err, "Failed to send initial HTTP request to agent")
		http.Error(w, "Failed to establish tunnel", http.StatusBadGateway)
		return
	}

	// Note: We removed the immediate error check here because it was consuming
	// the first packet from the packet connection, causing data loss. Instead, we'll let
	// the forwardTraffic method handle any errors that occur during data transfer.

	// Hijack the connection
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		klog.ErrorS(err, "Failed to hijack HTTP connection")
		return
	}
	defer clientConn.Close()

	klog.V(4).InfoS("Established HTTP tunnel", "cluster", clusterName, "packet_connection_id", pc.ID())

	// Start transparent data forwarding between client and agent
	h.forwardTraffic(ctx, clientConn, pc)
}

// forwardTraffic handles bidirectional data forwarding between client and agent
func (h *httpHandler) forwardTraffic(ctx context.Context, clientConn net.Conn, packetConnection *packetConnection) {
	// Create error channel for goroutines
	errChan := make(chan error, 2)

	// Forward data from client to agent
	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.ErrorS(fmt.Errorf("panic in client->agent forwarding: %v", r), "Panic in forwardTraffic")
			}
		}()
		errChan <- h.forwardClientToAgent(clientConn, packetConnection)
	}()

	// Forward data from agent to client
	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.ErrorS(fmt.Errorf("panic in agent->client forwarding: %v", r), "Panic in forwardTraffic")
			}
		}()
		errChan <- h.forwardAgentToClient(packetConnection, clientConn)
	}()

	// Wait for either direction to complete or error
	select {
	case err := <-errChan:
		if err != nil && err != io.EOF {
			klog.V(4).InfoS("Traffic forwarding ended", "error", err)
		}
	case <-ctx.Done():
		klog.V(4).InfoS("Traffic forwarding cancelled", "error", ctx.Err())
	}

	klog.V(4).InfoS("HTTP tunnel closed", "packet_connection_id", packetConnection.ID())
}

// packetSender interface for sending packets (used for testing)
type packetSender interface {
	ID() int64
	Send(packet *v1.Packet) error
}

// sendInitialHTTPRequest sends the original HTTP request to the agent to establish the connection
func (h *httpHandler) sendInitialHTTPRequest(pc packetSender, r *http.Request) error {
	// Build the complete HTTP request
	var requestData []byte

	// Build the HTTP request line with original protocol version
	// This preserves the original HTTP version (HTTP/1.0, HTTP/1.1, HTTP/2, etc.)
	// which is crucial for protocols like SPDY used by kubectl exec
	httpVersion := "HTTP/1.1" // Default fallback
	if r.ProtoMajor != 0 || r.ProtoMinor != 0 {
		httpVersion = fmt.Sprintf("HTTP/%d.%d", r.ProtoMajor, r.ProtoMinor)
	}

	requestLine := fmt.Sprintf("%s %s %s\r\n", r.Method, r.URL.RequestURI(), httpVersion)
	requestData = append(requestData, []byte(requestLine)...)

	// Add HTTP headers
	// Ensure Host header is present (required for HTTP/1.1 and later)
	if r.Header.Get("Host") == "" {
		// Use the original request's host
		hostHeader := fmt.Sprintf("Host: %s\r\n", r.Host)
		requestData = append(requestData, []byte(hostHeader)...)
	}

	for name, values := range r.Header {
		for _, value := range values {
			headerLine := fmt.Sprintf("%s: %s\r\n", name, value)
			requestData = append(requestData, []byte(headerLine)...)
		}
	}

	// Add empty line to separate headers from body
	requestData = append(requestData, []byte("\r\n")...)

	// Read and add request body
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("failed to read request body: %w", err)
		}
		r.Body.Close()
		requestData = append(requestData, bodyBytes...)
	}

	// Send the HTTP request as a data packet
	// NOTE: TargetAddress is required here because this is part of the connection
	// establishment phase. The agent needs to know the target service address
	// when processing the initial HTTP request.
	packet := &v1.Packet{
		ConnId: pc.ID(),
		Code:   v1.ControlCode_DATA,
		Data:   requestData,
	}

	return pc.Send(packet)
}

// forwardClientToAgent forwards data from client connection to packet connection
func (h *httpHandler) forwardClientToAgent(clientConn net.Conn, pc *packetConnection) error {
	buffer := make([]byte, 32*1024) // 32KB buffer

	for {
		n, err := clientConn.Read(buffer)
		if err != nil {
			if err == io.EOF {
				klog.V(4).InfoS("Client connection closed", "packet_connection_id", pc.ID())
			} else {
				klog.V(4).InfoS("Error reading from client", "packet_connection_id", pc.ID(), "error", err)
			}
			return err
		}

		if n > 0 {
			// Create a copy of the data to avoid race conditions
			// The buffer slice is reused in the next iteration, so we need to copy
			// the data to prevent concurrent access to the same memory
			data := make([]byte, n)
			copy(data, buffer[:n])

			// NOTE: TargetAddress is NOT set here because this is a data forwarding packet.
			// The connection has already been established, and the agent knows where to
			// forward this data. Setting TargetAddress would be redundant and inefficient.
			packet := &v1.Packet{
				ConnId: pc.ID(),
				Code:   v1.ControlCode_DATA,
				Data:   data,
			}

			if err := pc.Send(packet); err != nil {
				klog.ErrorS(err, "Failed to send data to agent", "packet_connection_id", pc.ID())
				return err
			}
			klog.V(5).InfoS("Forwarded data to agent", "packet_connection_id", pc.ID(), "bytes", n)
		}
	}
}

// forwardAgentToClient forwards data from packet connection to client connection
func (h *httpHandler) forwardAgentToClient(pc *packetConnection, clientConn net.Conn) error {
	for {
		packet := <-pc.Recv()
		if packet == nil {
			klog.V(4).InfoS("packet connection closed", "packet_connection_id", pc.ID())
			return io.EOF
		}

		if packet.Code == v1.ControlCode_ERROR {
			klog.ErrorS(fmt.Errorf("%s", packet.ErrorMessage), "Received error from agent", "packet_connection_id", pc.ID())

			// Send HTTP 502 Bad Gateway response for connection errors
			errorResponse := "HTTP/1.1 502 Bad Gateway\r\n" +
				"Content-Type: text/plain\r\n" +
				"Content-Length: " + fmt.Sprintf("%d", len(packet.ErrorMessage)) + "\r\n" +
				"Connection: close\r\n" +
				"\r\n" +
				packet.ErrorMessage

			_, writeErr := clientConn.Write([]byte(errorResponse))
			if writeErr != nil {
				klog.ErrorS(writeErr, "Failed to write error response to client", "packet_connection_id", pc.ID())
			}

			return fmt.Errorf("agent error: %s", packet.ErrorMessage)
		}

		if len(packet.Data) > 0 {
			_, err := clientConn.Write(packet.Data)
			if err != nil {
				klog.ErrorS(err, "Failed to write data to client", "packet_connection_id", pc.ID())
				return err
			}
			klog.V(5).InfoS("Forwarded data to client", "packet_connection_id", pc.ID(), "bytes", len(packet.Data))
		}
	}
}
