package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

type Tunnel struct {
	id          string
	clusterName string
	grpcStream  v1.TunnelService_TunnelServer
	ctx         context.Context
	createdAt   time.Time

	// packet connection management
	mu               sync.RWMutex
	packetConns      map[int64]*packetConnection
	nextPacketConnID int64
	outgoingChan     chan *v1.Packet
	closed           bool
	initialized      int32 // atomic flag to check if connection is initialized
}

// ID returns the unique identifier for this connection
func (t *Tunnel) ID() string {
	return t.id
}

// ClusterName returns the name of the cluster this connection belongs to
func (t *Tunnel) ClusterName() string {
	return t.clusterName
}

// Serve handles the connection (blocks until connection is closed)
func (t *Tunnel) Serve() error {
	klog.InfoS("Starting to serve tunnel", "cluster", t.clusterName, "tunnel_id", t.id)

	// Initialize connection with proper synchronization
	t.mu.Lock()
	t.outgoingChan = make(chan *v1.Packet, 1000) // Buffer for outgoing packets
	t.packetConns = make(map[int64]*packetConnection)
	atomic.StoreInt32(&t.initialized, 1) // Mark as initialized
	t.mu.Unlock()

	// Start goroutines for handling incoming and outgoing packets
	errCh := make(chan error, 2)

	// Goroutine 1: Handle incoming packets from agent
	go func() {
		errCh <- t.handleIncoming()
	}()

	// Goroutine 2: Handle outgoing packets to agent
	go func() {
		errCh <- t.handleOutgoing()
	}()

	// Wait for either goroutine to exit
	err := <-errCh

	// Clean up
	t.Close()

	return err
}

// handleIncoming processes packets received from the agent
func (t *Tunnel) handleIncoming() error {
	for {
		packet, err := t.grpcStream.Recv()
		if err != nil {
			klog.InfoS("Connection receive ended", "cluster", t.clusterName, "tunnel_id", t.id, "error", err)
			return err
		}

		// Handle different packet types
		switch packet.Code {
		case v1.ControlCode_DATA:
			t.handleDataPacket(packet)
		case v1.ControlCode_ERROR:
			t.handleErrorPacket(packet)
		case v1.ControlCode_DRAIN:
			klog.InfoS("Received DRAIN signal from agent", "cluster", t.clusterName, "tunnel_id", t.id)
			return fmt.Errorf("agent initiated drain")
		default:
			klog.Warningf("Unknown packet code received: %v", packet.Code)
		}
	}
}

// handleOutgoing sends packets to the agent
func (t *Tunnel) handleOutgoing() error {
	for {
		select {
		case packet := <-t.outgoingChan:
			if err := t.grpcStream.Send(packet); err != nil {
				klog.ErrorS(err, "Failed to send packet to agent", "cluster", t.clusterName, "tunnel_id", t.id)
				return err
			}
		case <-t.ctx.Done():
			return t.ctx.Err()
		}
	}
}

// handleDataPacket processes a DATA packet
func (t *Tunnel) handleDataPacket(packet *v1.Packet) {
	t.mu.RLock()
	pc, exists := t.packetConns[packet.ConnId]
	t.mu.RUnlock()

	if exists {
		// Use a safe send that handles closed channels
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Channel was closed, ignore the packet
					klog.V(4).InfoS("Dropping packet for closed packet connection", "packet_connection_id", packet.ConnId)
				}
			}()

			// Check if packet connection context is cancelled (connection closed)
			select {
			case <-pc.ctx.Done():
				// Stream is closed, drop the packet
				klog.V(4).InfoS("Dropping packet for closed packet connection", "packet_connection_id", packet.ConnId)
			default:
				// Send to existing packet connection
				select {
				case pc.incomingChan <- packet:
				case <-pc.ctx.Done():
					// Stream was closed while we were trying to send
					klog.V(4).InfoS("Dropping packet for closed packet connection", "packet_connection_id", packet.ConnId)
				default:
					klog.Warningf("Stream %d incoming channel is full, dropping packet", packet.ConnId)
				}
			}
		}()
	} else {
		klog.Warningf("Received packet for unknown packet connection %d", packet.ConnId)
		// Send error response
		errorPacket := &v1.Packet{
			ConnId:       packet.ConnId,
			Code:         v1.ControlCode_ERROR,
			ErrorMessage: fmt.Sprintf("unknown packet connection %d", packet.ConnId),
		}
		select {
		case t.outgoingChan <- errorPacket:
		default:
			klog.Warningf("Outgoing channel is full, dropping error packet")
		}
	}
}

// handleErrorPacket processes an ERROR packet
func (t *Tunnel) handleErrorPacket(packet *v1.Packet) {
	t.mu.RLock()
	pc, exists := t.packetConns[packet.ConnId]
	t.mu.RUnlock()

	if exists {
		// Use a safe send that handles closed channels gracefully
		t.safeSendToStream(pc, packet)
	}
}

// safeSendToStream safely sends a packet to a packet connection's incoming channel
func (t *Tunnel) safeSendToStream(pc *packetConnection, packet *v1.Packet) {
	// Use a defer/recover to handle potential panic from sending to closed channel
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed while we were trying to send
			klog.V(4).InfoS("Packet dropped due to closed channel", "packet_connection_id", packet.ConnId)
		}
	}()

	// Check if the packet connection context is cancelled (connection closed)
	select {
	case <-pc.ctx.Done():
		klog.V(4).InfoS("Dropping packet for closed packet connection", "packet_connection_id", packet.ConnId)
		return
	default:
		// Context is not cancelled, proceed with sending
	}

	// Try to send the packet with a non-blocking send
	select {
	case pc.incomingChan <- packet:
		// Successfully sent
	case <-pc.ctx.Done():
		// Stream was closed while we were trying to send
		klog.V(4).InfoS("Dropping packet for closed packet connection", "packet_connection_id", packet.ConnId)
	default:
		// Channel is full, drop the packet
		klog.V(4).InfoS("Dropping packet for full packet connection", "packet_connection_id", packet.ConnId)
	}
}

// NewPacketConn creates a new PacketStream using this connection
func (t *Tunnel) NewPacketConn(ctx context.Context) (*packetConnection, error) {
	// Check if connection is initialized
	if atomic.LoadInt32(&t.initialized) == 0 {
		return nil, fmt.Errorf("connection not initialized")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, fmt.Errorf("connection is closed")
	}

	// Generate new packet connection ID
	packetConnID := atomic.AddInt64(&t.nextPacketConnID, 1)

	// Create context with cancel for this packet connection
	packetCtx, cancel := context.WithCancel(ctx)

	// Create new packet connection
	packetConn := &packetConnection{
		id:           packetConnID,
		ctx:          packetCtx,
		cancel:       cancel,
		tunnel:       t,
		incomingChan: make(chan *v1.Packet, 100),
		closed:       false,
	}

	// Note: We don't close the incomingChan here to avoid data races.
	// The channel will be garbage collected when the packetConnection is no longer referenced.
	// The context cancellation is sufficient to signal that the connection is closed.

	// Register packet connection
	t.packetConns[packetConnID] = packetConn

	klog.V(4).InfoS("Created new packet connection", "cluster", t.clusterName, "tunnel_id", t.id, "packet_connection_id", packetConnID)

	return packetConn, nil
}

// removePacketConn removes a packet connection from this tunnel
func (t *Tunnel) removePacketConn(packetConnID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.packetConns, packetConnID)
	klog.V(4).InfoS("Removed packet connection", "cluster", t.clusterName, "tunnel_id", t.id, "packet_connection_id", packetConnID)
}

// sendPacket sends a packet through this connection
func (t *Tunnel) sendPacket(packet *v1.Packet) error {
	// Check if connection is initialized
	if atomic.LoadInt32(&t.initialized) == 0 {
		return fmt.Errorf("connection not initialized")
	}

	// Use read lock to safely access outgoingChan
	t.mu.RLock()
	outgoingChan := t.outgoingChan
	t.mu.RUnlock()

	if outgoingChan == nil {
		return fmt.Errorf("connection not ready")
	}

	select {
	case outgoingChan <- packet:
		return nil
	case <-t.ctx.Done():
		return t.ctx.Err()
	default:
		return fmt.Errorf("outgoing channel is full")
	}
}

// Close closes the connection
func (t *Tunnel) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	t.closed = true

	// Close all packet connections
	for _, packetConn := range t.packetConns {
		packetConn.closeWithError(fmt.Errorf("connection closed"))
	}
	t.packetConns = make(map[int64]*packetConnection)

	// Close outgoing channel
	if t.outgoingChan != nil {
		close(t.outgoingChan)
	}

	klog.InfoS("Closed tunnel", "cluster", t.clusterName, "tunnel_id", t.id)
}
