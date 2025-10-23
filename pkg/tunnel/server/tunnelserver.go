package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

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

// TunnelServer implements the hub-side tunnel server with both gRPC and HTTP servers
type TunnelServer struct {
	tunnelManager *TunnelManager

	// user-server is a https server
	http.Handler

	// Embed the unimplemented server to satisfy the interface
	v1.UnimplementedTunnelServiceServer
}

// New creates a new Hub server instance
func New(parser ClusterNameParser) (*TunnelServer, error) {
	// Create tunnel manager
	tunnelManager := NewTunnelManager()
	return &TunnelServer{
		tunnelManager: tunnelManager,
		Handler: &userServerHTTPHandler{
			tunnelManager: tunnelManager,
			parser:        parser,
		},
	}, nil
}

// GetTunnel returns the tunnel for a specific cluster
func (s *TunnelServer) GetTunnel(clusterName string) *Tunnel {
	if s.tunnelManager == nil {
		return nil
	}
	return s.tunnelManager.GetTunnel(clusterName)
}

// Tunnel implements the TunnelService gRPC interface
// This is called when an agent establishes a tunnel
func (s *TunnelServer) Tunnel(stream v1.TunnelService_TunnelServer) error {
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

// userServerHTTPHandler implements http.Handler and handles HTTP requests using Router
type userServerHTTPHandler struct {
	tunnelManager *TunnelManager
	parser        ClusterNameParser
}

// ServeHTTP handles HTTP requests and routes them to appropriate clusters using HTTP CONNECT tunneling
func (h *userServerHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
func (h *userServerHTTPHandler) forwardTraffic(ctx context.Context, clientConn net.Conn, packetConnection *packetConnection) {
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
func (h *userServerHTTPHandler) sendInitialHTTPRequest(pc packetSender, r *http.Request) error {
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
func (h *userServerHTTPHandler) forwardClientToAgent(clientConn net.Conn, pc *packetConnection) error {
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
func (h *userServerHTTPHandler) forwardAgentToClient(pc *packetConnection, clientConn net.Conn) error {
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

func RunTunnelUserServer(ctx context.Context, ts *TunnelServer, address string, tlsConfig *tls.Config) error {
	server := &http.Server{
		Addr:      address,
		Handler:   ts,
		TLSConfig: tlsConfig,
		// Disable automatic HTTP/2 upgrade to support SPDY protocol used by kubectl exec
		// HTTP/2 cannot upgrade to SPDY, so we need to prevent automatic HTTP/2 negotiation
		// This allows clients like kubectl to use SPDY for exec/port-forward operations
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	go func() {
		<-ctx.Done()
		klog.InfoS("Shutting down tunnel user server", "address", address)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.ErrorS(err, "Error shutting down tunnel user server", "address", address)
		}
	}()

	klog.InfoS("Starting tunnel user server", "address", address)
	var err error
	if tlsConfig != nil {
		err = server.ListenAndServeTLS("", "")
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start tunnel user server: %w", err)
	}
	return nil
}
