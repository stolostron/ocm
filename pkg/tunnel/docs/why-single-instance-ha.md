# Why mctunnel Hub Should Use Single Instance with Fast Recovery Instead of Multi-Replica HA

## Executive Summary

This document explains why the mctunnel Hub should remain a **single-instance service** with **fast failure detection and recovery** rather than implementing traditional multi-replica high availability (HA). The decision is based on the unique characteristics of network proxy services with real-time TCP connection state.

## The Statefulness Challenge

### Types of Stateful Services in Industry

| Service Type | State Characteristics | HA Strategy | Complexity |
|--------------|----------------------|-------------|------------|
| **etcd** | Partitionable, Replicable Data | Multi-node consensus (Raft) | Medium |
| **Kafka Controller** | Reconstructible Coordination State | Active-standby with leader election | Low |
| **Kubernetes API Server** | Stateless frontend + External storage | Stateless replicas behind load balancer | Low |
| **mctunnel Hub** | **Real-time TCP connection state** | **Single instance + fast recovery** | **Most practical** |
| **Load Balancers** | Connection tracking tables | Distributed with consistent hashing | High |

### Why mctunnel is Special

Unlike traditional stateful services, mctunnel Hub manages **real-time network connection state** that has unique constraints:

1. **Non-serializable State**: Active TCP sockets cannot be serialized, migrated, or replicated
2. **Millisecond-level Latency Requirements**: Any state synchronization delay causes connection timeouts
3. **Network Location Sensitivity**: Each TCP connection is bound to specific network endpoints
4. **File Descriptor Dependencies**: Connection state includes kernel-level file descriptors and buffers

## Industry Precedents

### Network Proxies with Similar Constraints

- **HAProxy/NGINX**: Traditional approach uses **keepalived** with virtual IP and rapid failover
- **Envoy Proxy**: Control plane can be HA, but data plane runs as independent instances
- **AWS ELB/NLB**: Distributed load balancers use consistent hashing but **cannot guarantee connection continuity**
- **Kubernetes kube-proxy**: Each node runs independent instance with no shared state

### Key Insight
Services managing **live network connections** universally choose **single-instance + fast recovery** over **multi-replica HA** due to the fundamental impossibility of connection state migration.

## Recommended HA Strategy: Single Instance + Fast Recovery

### 1. Kubernetes Native Health Checks

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: mctunnel-hub
  labels:
    app: mctunnel-hub
spec:
  containers:
  - name: hub
    image: mctunnel/hub:latest
    ports:
    - containerPort: 8443
    livenessProbe:
      grpc:
        port: 8443
      initialDelaySeconds: 10
      periodSeconds: 5
      timeoutSeconds: 3
      failureThreshold: 3
    readinessProbe:
      grpc:
        port: 8443
      initialDelaySeconds: 5
      periodSeconds: 2
      timeoutSeconds: 2
      failureThreshold: 2
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: mctunnel-hub-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: mctunnel-hub
```

### 2. Fast Failure Detection

- **Readiness Probe**: 2-second intervals with 2-failure threshold (4-second detection)
- **Liveness Probe**: 5-second intervals with 3-failure threshold (15-second detection)
- **gRPC Health Checks**: Native protocol support for accurate service health

### 3. Rapid Recovery Mechanisms

#### Agent-Side Resilience
```go
// Automatic reconnection with exponential backoff
func (a *Agent) handleDisconnection() {
    backoff := time.Second
    for {
        if err := a.connectToHub(); err == nil {
            break
        }
        time.Sleep(backoff)
        backoff = min(backoff*2, time.Minute)
    }
}
```

#### Kubernetes Deployment Strategy
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mctunnel-hub
spec:
  replicas: 1  # Single instance
  strategy:
    type: Recreate  # Ensure clean restart
  template:
    spec:
      terminationGracePeriodSeconds: 30  # Allow graceful connection cleanup
```

## Trade-off Analysis

| Approach | Connection Continuity | Implementation Complexity | Operational Overhead | Recovery Time |
|----------|----------------------|---------------------------|---------------------|---------------|
| **Single Instance + Fast Recovery** | Brief interruption (<30s) | Low | Minimal | 15-30 seconds |
| **Multi-Replica HA** | Theoretical zero-downtime | Extremely High | Complex state sync | Complex debugging |

## Monitoring and Alerting

### Key Metrics to Track
- **Pod restart frequency**: Should be rare under normal conditions
- **Agent reconnection rate**: Indicates hub stability
- **Connection establishment latency**: Measures recovery performance
- **gRPC health check failures**: Early warning indicator

### Recommended Alerts
```yaml
# Prometheus alerting rules
- alert: MCTunnelHubDown
  expr: up{job="mctunnel-hub"} == 0
  for: 15s

- alert: HighAgentReconnectionRate
  expr: rate(mctunnel_agent_reconnections_total[5m]) > 0.1
  for: 2m
```

## Conclusion

The mctunnel Hub should remain a **single-instance service** because:

1. **Technical Reality**: Real-time TCP connection state cannot be replicated or migrated
2. **Simplicity**: Single-instance design significantly reduces operational complexity
3. **Proven Pattern**: Industry network proxies use similar single-instance + fast recovery strategies
4. **Kubernetes Native**: Leverages built-in health checks and rapid pod restart capabilities

This approach provides **practical high availability** through fast failure detection and recovery rather than attempting impossible connection state replication.

## References

- [Kubernetes Health Checks Documentation](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)
- [HAProxy High Availability Guide](http://www.haproxy.org/download/1.8/doc/management.txt)
- [Envoy Proxy Architecture Overview](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/arch_overview)