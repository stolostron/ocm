package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"
	v1 "open-cluster-management.io/ocm/pkg/tunnel/api/v1"
)

// TunnelManager manages all tunnels from agents
type TunnelManager struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel // clusterName -> tunnels
}

// NewTunnelManager creates a new tunnel manager
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels: make(map[string]*Tunnel),
	}
}

// NewTunnel creates a new tunnel for an agent
func (tm *TunnelManager) NewTunnel(ctx context.Context, clusterName string, stream v1.TunnelService_TunnelServer) (*Tunnel, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Check if there's already a tunnel for this cluster
	if existingTunnel, exists := tm.tunnels[clusterName]; exists {
		klog.InfoS("Replacing existing tunnel for cluster", "cluster", clusterName, "old_tunnel_id", existingTunnel.ID())
		// Close the existing tunnel
		existingTunnel.Close()
	}

	// Create new tunnel
	t := &Tunnel{
		id:          generateTunnelID(),
		clusterName: clusterName,
		grpcStream:  stream,
		ctx:         ctx,
		createdAt:   time.Now(),
	}

	// Store the tunnel
	tm.tunnels[clusterName] = t

	klog.InfoS("Created new tunnel for cluster", "cluster", clusterName, "tunnel_id", t.id)

	return t, nil
}

// GetTunnel returns the tunnel for a specific cluster
func (tm *TunnelManager) GetTunnel(clusterName string) *Tunnel {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	tunnel, exists := tm.tunnels[clusterName]
	if !exists {
		return nil
	}

	return tunnel
}

// RemoveTunnel removes a tunnel for a cluster
func (tm *TunnelManager) RemoveTunnel(clusterName string, tunnelID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	t, exists := tm.tunnels[clusterName]
	if !exists {
		return
	}

	// Only remove if the tunnel ID matches (to handle race conditions)
	if t.ID() == tunnelID {
		delete(tm.tunnels, clusterName)
		klog.InfoS("Removed tunnel for cluster", "cluster", clusterName, "tunnel_id", tunnelID)
	}
}

// Close closes all tunnels
func (tm *TunnelManager) Close() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for clusterName, t := range tm.tunnels {
		t.Close()
		klog.InfoS("Closed tunnel", "cluster", clusterName, "tunnel_id", t.ID())
	}

	tm.tunnels = make(map[string]*Tunnel)
}

// generateTunnelID generates a unique tunnel ID
func generateTunnelID() string {
	// Simple implementation - in production, use UUID or similar
	return fmt.Sprintf("tunnel-%d", time.Now().UnixNano())
}
