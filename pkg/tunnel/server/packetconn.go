package server

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

type packetConnection struct {
	id           int64
	ctx          context.Context
	cancel       context.CancelFunc
	tunnel       *Tunnel
	incomingChan chan *v1.Packet
	mu           sync.Mutex
	closed       bool
	closeError   error
}

// Context returns the context associated with this packet connection
func (pc *packetConnection) Context() context.Context {
	return pc.ctx
}

// ID returns the unique identifier for this packet connection
func (pc *packetConnection) ID() int64 {
	return pc.id
}

// Recv returns a channel for receiving packets from the agent
func (pc *packetConnection) Recv() <-chan *v1.Packet {
	return pc.incomingChan
}

// Send sends a packet to the agent
func (pc *packetConnection) Send(packet *v1.Packet) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.closed {
		return fmt.Errorf("packet connection is closed: %v", pc.closeError)
	}

	// Set the packet connection ID
	packet.ConnId = pc.id

	// Send through the tunnel
	return pc.tunnel.sendPacket(packet)
}

// Close closes the packet connection with an optional error
func (pc *packetConnection) Close(err error) {
	pc.closeWithError(err)
}

// closeWithError closes the packet connection with a specific error
func (pc *packetConnection) closeWithError(err error) {
	pc.mu.Lock()

	if pc.closed {
		pc.mu.Unlock()
		return
	}

	pc.closed = true
	pc.closeError = err

	// Cancel the context to signal closure
	if pc.cancel != nil {
		pc.cancel()
	}

	pc.mu.Unlock()

	// Remove from tunnel - do this outside the lock to avoid deadlock
	pc.tunnel.removePacketConn(pc.id)

	if err != nil {
		klog.V(4).InfoS("Closed packet connection with error", "packet_connection_id", pc.id, "error", err)
	} else {
		klog.V(4).InfoS("Closed packet connection", "packet_connection_id", pc.id)
	}
}
