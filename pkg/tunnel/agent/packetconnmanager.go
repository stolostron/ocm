package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

const (
	// outgoingChanSize is the buffer size for the outgoing packet channel
	outgoingChanSize = 150
	// incomingChanSize is the buffer size for each connection's incoming packet channel
	// This ensures packets with the same conn_id are processed sequentially while
	// allowing buffering to handle network fluctuations and prevent blocking
	incomingChanSize = 150
	// connReadBufferSize is the buffer size for reading from local connections
	// 32KB is a good balance between memory usage and performance for most use cases:
	// - Small enough to avoid excessive memory usage
	// - Large enough to reduce syscall overhead
	// - Suitable for typical API requests/responses
	// For high-throughput scenarios, consider increasing to 64KB or 128KB
	connReadBufferSize = 32 * 1024 // 32KB
	// dialTimeout is the timeout for dialing local services
	dialTimeout = 10 * time.Second

	udsSocketPath = "/tmp/multiclustertunnel.sock"
)

// PacketConnManagerConfig holds configuration for the packetConnManagerImpl
type PacketConnManagerConfig struct {
	// ReadBufferSize is the buffer size for reading from local connections
	// Default: 32KB, recommended range: 16KB-128KB
	ReadBufferSize int
	// OutgoingChanSize is the buffer size for the outgoing packet channel
	// Default: 150, recommended range: 50-500
	OutgoingChanSize int
	// IncomingChanSize is the buffer size for each connection's incoming packet channel
	// Default: 150, recommended range: 20-200
	IncomingChanSize int
	// DialTimeout is the timeout for dialing local services
	// Default: 10s, recommended range: 5s-30s
	DialTimeout time.Duration
	// UDSSocketPath is the path to the Unix Domain Socket for connecting to the proxy
	// Default: "/tmp/multiclustertunnel.sock"
	UDSSocketPath string
}

// DefaultPacketConnManagerConfig returns the default configuration
func DefaultPacketConnManagerConfig() *PacketConnManagerConfig {
	return &PacketConnManagerConfig{
		ReadBufferSize:   connReadBufferSize,
		OutgoingChanSize: outgoingChanSize,
		IncomingChanSize: incomingChanSize,
		DialTimeout:      dialTimeout,
		UDSSocketPath:    udsSocketPath,
	}
}

// packetConnManager receives tunnel.Packet from Hub and manages local connections
type packetConnManager interface {
	Dispatch(packet *v1.Packet) error
	OutgoingChan() <-chan *v1.Packet
	Close() error
}

// packetConn represents a single local connection managed by the packetConnManager
type packetConn struct {
	id       int64
	conn     net.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	outgoing chan<- *v1.Packet
	// incoming is the channel for packets from Hub that need to be processed sequentially
	// This ensures packets with the same conn_id are processed in order
	incoming chan *v1.Packet
	// incomingClosed tracks if the incoming channel has been closed to prevent double-close
	incomingClosed int32 // atomic flag
	// closeOnce ensures the channel is only closed once
	closeOnce sync.Once
}

type packetConnManagerImpl struct {
	config           *PacketConnManagerConfig
	localConnections map[int64]*packetConn
	connLock         sync.RWMutex
	outgoing         chan *v1.Packet
	ctx              context.Context
	cancel           context.CancelFunc
}

func newPacketConnectionManagerWithSocketPath(ctx context.Context, udsSocketPath string) packetConnManager {
	config := DefaultPacketConnManagerConfig()
	config.UDSSocketPath = udsSocketPath
	return newPacketConnectionManagerWithConfig(ctx, config)
}

func newPacketConnectionManagerWithConfig(ctx context.Context, config *PacketConnManagerConfig) packetConnManager {
	ctx, cancel := context.WithCancel(ctx)
	return &packetConnManagerImpl{
		config:           config,
		localConnections: make(map[int64]*packetConn),
		outgoing:         make(chan *v1.Packet, config.OutgoingChanSize),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// Dispatch handles incoming packets from the Hub
func (p *packetConnManagerImpl) Dispatch(packet *v1.Packet) error {
	klog.V(4).InfoS("Received packet from Hub", "conn_id", packet.ConnId, "code", packet.Code, "data_size", len(packet.Data))

	switch packet.Code {
	case v1.ControlCode_DATA:
		return p.handleDataPacket(packet)
	case v1.ControlCode_ERROR:
		return p.handleErrorPacket(packet)
	default:
		return fmt.Errorf("unknown control code: %v", packet.Code)
	}
}

// OutgoingChan returns the channel for outgoing packets to the Hub
func (p *packetConnManagerImpl) OutgoingChan() <-chan *v1.Packet {
	return p.outgoing
}

// Close gracefully shuts down the connection manager
func (p *packetConnManagerImpl) Close() error {
	p.cancel()

	// Close all active connections
	p.connLock.Lock()
	for _, conn := range p.localConnections {
		conn.cancel()
		conn.conn.Close()
	}
	p.localConnections = make(map[int64]*packetConn)
	p.connLock.Unlock()

	// Close the outgoing channel
	close(p.outgoing)

	return nil
}

// handleDataPacket processes DATA packets from the Hub
// This method is now non-blocking and dispatches packets to per-connection channels
func (p *packetConnManagerImpl) handleDataPacket(packet *v1.Packet) error {
	connID := packet.ConnId

	p.connLock.RLock()
	lc, exists := p.localConnections[connID]
	p.connLock.RUnlock()

	if !exists {
		// This is a new connection, create it
		return p.createConnection(packet)
	}

	// Send packet to connection's incoming channel for sequential processing
	// Use a safe send function to handle potential channel closure
	return p.safeSendToConnection(lc, packet, connID)
}

// safeSendToConnection safely sends a packet to a connection's incoming channel
// It handles the race condition between sending and channel closure
func (p *packetConnManagerImpl) safeSendToConnection(lc *packetConn, packet *v1.Packet, connID int64) error {
	// Use a defer/recover to catch panics from sending on closed channels
	defer func() {
		if r := recover(); r != nil {
			// This can happen if the channel is closed between our check and send
			klog.V(4).InfoS("Recovered from panic when sending to connection", "conn_id", connID, "panic", r)
		}
	}()

	// First, try to send without blocking
	select {
	case lc.incoming <- packet:
		return nil
	case <-lc.ctx.Done():
		return fmt.Errorf("local connection %d is closing", connID)
	case <-p.ctx.Done():
		return fmt.Errorf("local connection manager is closing")
	default:
		// Channel might be full or closed, check the atomic flag
		if atomic.LoadInt32(&lc.incomingClosed) == 1 {
			return fmt.Errorf("connection %d is already closed", connID)
		}

		// Try again with a timeout
		select {
		case lc.incoming <- packet:
			return nil
		case <-lc.ctx.Done():
			return fmt.Errorf("local connection %d is closing", connID)
		case <-p.ctx.Done():
			return fmt.Errorf("local connection manager is closing")
		case <-time.After(100 * time.Millisecond):
			return fmt.Errorf("timeout sending packet to connection %d", connID)
		}
	}
}

// handleErrorPacket processes ERROR packets from the Hub
func (p *packetConnManagerImpl) handleErrorPacket(packet *v1.Packet) error {
	connID := packet.ConnId

	// Log the error
	klog.ErrorS(fmt.Errorf("%s", packet.ErrorMessage), "Received error from Hub", "conn_id", connID)

	// Close the connection if it exists
	// Note: This can race with readFromConnection/processIncomingPackets
	// if local connection errors occur simultaneously with Hub errors
	p.removeConnection(connID)

	return nil
}

// createConnection establishes a new connection to the target service
func (p *packetConnManagerImpl) createConnection(packet *v1.Packet) error {
	connID := packet.ConnId

	klog.V(4).InfoS("Target address resolved", "conn_id", connID)

	// Dial the target service
	conn, err := net.DialTimeout("unix", p.config.UDSSocketPath, p.config.DialTimeout)
	if err != nil {
		// Send error response back to Hub instead of just returning error
		errorPacket := &v1.Packet{
			ConnId:       connID,
			Code:         v1.ControlCode_ERROR,
			ErrorMessage: fmt.Sprintf("Connection failed: %v", err),
		}

		// Send error packet to Hub
		select {
		case p.outgoing <- errorPacket:
		case <-p.ctx.Done():
			// Context cancelled, don't block
		default:
			// Channel full, log warning but don't block
			klog.Warningf("Failed to send error packet for conn_id %d: outgoing channel full", connID)
		}

		return fmt.Errorf("failed to dial for conn_id %d: %w", connID, err)
	}
	klog.V(4).InfoS("Successfully connected to target", "conn_id", connID)

	// Create connection context
	ctx, cancel := context.WithCancel(p.ctx)

	// Create lc object with incoming packet channel
	lc := &packetConn{
		id:             connID,
		conn:           conn,
		ctx:            ctx,
		cancel:         cancel,
		outgoing:       p.outgoing,
		incoming:       make(chan *v1.Packet, p.config.IncomingChanSize),
		incomingClosed: 0, // Initialize atomic flag
	}

	// Send the initial packet to the connection's incoming channel BEFORE starting goroutines
	// This prevents race condition where readFromConnection might call removeConnection
	// before we can send the initial packet
	select {
	case lc.incoming <- packet:
	case <-ctx.Done():
		// Context cancelled during connection setup
		return fmt.Errorf("failed to send initial packet to connection %d: context cancelled", connID)
	}

	// Store the connection
	p.connLock.Lock()
	p.localConnections[connID] = lc
	p.connLock.Unlock()

	// Start goroutine to read from the connection and send data back to Hub
	go p.readFromConnection(lc)

	// Start goroutine to process incoming packets sequentially for this connection
	go p.processIncomingPackets(lc)

	klog.V(4).InfoS("Created new connection", "conn_id", connID)
	return nil
}

// removeConnection closes and removes a connection
// This method can be called concurrently from multiple goroutines:
// 1. readFromConnection (defer cleanup when read fails)
// 2. processIncomingPackets (when write to target fails)
// 3. handleErrorPacket (when Hub sends ERROR packet)
// 4. createConnection (when initial packet send fails)
// 5. Close (when ProxyManager shuts down)
//
// Race condition example:
// - Target service suddenly closes connection
// - readFromConnection gets io.EOF and calls removeConnection via defer
// - processIncomingPackets gets "broken pipe" and calls removeConnection directly
// - Both goroutines may try to close conn.incoming channel simultaneously
func (p *packetConnManagerImpl) removeConnection(connID int64) {
	// Lock protects the connections map and ensures only one goroutine
	// can modify the connection state at a time
	p.connLock.Lock()
	defer p.connLock.Unlock()

	lc, exists := p.localConnections[connID]
	if !exists {
		// Connection already removed by another goroutine
		return
	}

	// Cancel the connection context first to signal all goroutines to stop
	lc.cancel()
	lc.conn.Close()

	// Close the incoming channel to signal the processing goroutine to exit
	// Use sync.Once to ensure the channel is only closed once
	lc.closeOnce.Do(func() {
		// Set the atomic flag first
		atomic.StoreInt32(&lc.incomingClosed, 1)
		// Close the channel - this is safe because sync.Once ensures
		// this code block only runs once
		close(lc.incoming)
	})

	// Remove from map to prevent future access
	delete(p.localConnections, connID)

	klog.V(4).InfoS("Removed connection", "conn_id", connID)
}

// readFromConnection reads data from a local connection and sends it to the Hub
func (p *packetConnManagerImpl) readFromConnection(lc *packetConn) {
	// Always cleanup connection when this goroutine exits (normal or error)
	// Note: This can race with processIncomingPackets calling removeConnection
	// when both encounter errors simultaneously (e.g., target service crash)
	defer p.removeConnection(lc.id)

	buffer := make([]byte, p.config.ReadBufferSize)

	for {
		select {
		case <-lc.ctx.Done():
			return
		default:
			// Set read deadline to avoid blocking forever
			lc.conn.SetReadDeadline(time.Now().Add(time.Second))

			n, err := lc.conn.Read(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout is expected, continue reading
					continue
				}
				if err == io.EOF {
					klog.V(4).InfoS("Connection closed by remote", "conn_id", lc.id)
				} else {
					klog.ErrorS(err, "Error reading from connection", "conn_id", lc.id)
				}
				return
			}

			if n > 0 {
				// Send data back to Hub
				packet := &v1.Packet{
					ConnId: lc.id,
					Code:   v1.ControlCode_DATA,
					Data:   make([]byte, n),
				}
				copy(packet.Data, buffer[:n])

				select {
				case lc.outgoing <- packet:
				case <-lc.ctx.Done():
					return
				case <-p.ctx.Done():
					return
				}
			}
		}
	}
}

// processIncomingPackets processes packets from Hub sequentially for a specific connection
// This ensures that packets with the same conn_id are processed in order
func (p *packetConnManagerImpl) processIncomingPackets(lc *packetConn) {
	defer func() {
		klog.V(4).InfoS("Stopped processing incoming packets", "conn_id", lc.id)
	}()

	klog.V(4).InfoS("Started processing incoming packets", "conn_id", lc.id)

	for {
		select {
		case packet, ok := <-lc.incoming:
			if !ok {
				// Channel is closed, connection is being removed
				return
			}

			// Process the packet by writing data to the target connection
			if len(packet.Data) > 0 {
				// Transparent data forwarding - no HTTP-specific processing needed
				_, err := lc.conn.Write(packet.Data)
				if err != nil {
					klog.ErrorS(err, "Failed to write data to target connection", "conn_id", lc.id)
					// Connection failed, clean it up
					// Note: This can race with readFromConnection's defer cleanup
					// if both goroutines encounter errors at the same time
					p.removeConnection(lc.id)
					return
				}
				klog.V(5).InfoS("Forwarded data to target", "conn_id", lc.id, "bytes", len(packet.Data))
			}

		case <-lc.ctx.Done():
			return
		case <-p.ctx.Done():
			return
		}
	}
}
