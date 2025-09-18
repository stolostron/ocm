# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Open Cluster Management (OCM) is a CNCF sandbox project that provides a vendor-neutral, standards-based framework for managing multiple Kubernetes clusters in a hub-spoke architecture. The project focuses on multicluster and multicloud scenarios with plug-and-play extensibility.

## Core Architecture

**Hub-Spoke Model**: Centralized control plane (hub) manages multiple clusters (spokes) through pull-based agents.

**Key Components**:
- **Registration**: Secure cluster registration with mTLS handshaking (`pkg/registration/`)
- **Work Distribution**: ManifestWork API for distributing resources (`pkg/work/`)
- **Placement**: Dynamic cluster selection and workload placement (`pkg/placement/`)
- **Add-on Framework**: Extensible framework for multicluster capabilities (`pkg/addon/`)
- **Operator**: Lifecycle management for OCM components (`pkg/operator/`)

**Critical APIs**:
- `ManagedCluster` - Represents a managed cluster
- `ManifestWork` - Work to be distributed to clusters
- `Placement` - Cluster selection and placement rules
- `ClusterManager` - Hub cluster configuration
- `Klusterlet` - Managed cluster agent configuration

## Build and Development Commands

**Build System**: Go 1.24+ with Make + OpenShift build machinery

```bash
# Core development cycle
make build                              # Build all components
make verify                            # Run all verification checks
make test-unit                         # Unit tests with coverage
make test-integration                  # Integration tests with envtest

# Build specific components
make image-registration                # Registration component
make image-work                        # Work distribution
make image-placement                   # Placement controller
make image-addon-manager               # Add-on manager
make image-registration-operator       # Registration operator

# Code quality
make verify-gocilint                   # Lint check
make verify-fmt-imports                # Import format check
make fmt-imports                       # Fix import formatting

# Update dependencies and CRDs
make update                            # Update CRDs and manifests
make copy-crd                          # Sync CRDs from API repo
```

**Container Images**: Built with `IMAGE_REGISTRY=quay.io/open-cluster-management IMAGE_TAG=test make images`

## Testing Strategy

**Framework**: Ginkgo + Gomega (BDD-style testing)

```bash
# Unit testing
make test-unit                         # All unit tests with race detection

# Integration testing (uses kubebuilder envtest)
make test-integration                  # All integration tests
make test-registration-integration     # Registration component only
make test-work-integration            # Work component only
make test-placement-integration       # Placement component only
make test-addon-integration           # Add-on component only

# E2E testing
make test-e2e                         # Full E2E suite
./solutions/setup-dev-environment/local-up.sh  # Local kind setup
```

**Integration Tests**: Use kubebuilder envtest with local Kubernetes API server. No external cluster dependencies.

## Key File Locations

**Entry Points**:
- `cmd/hub/` - Hub cluster control plane
- `cmd/spoke/` - Managed cluster agents
- `cmd/webhook/` - Validation webhooks

**Controllers**:
- `pkg/registration/hub/` - Cluster registration controllers
- `pkg/work/hub/` - ManifestWork distribution controllers
- `pkg/placement/controllers/` - Placement and scheduling controllers
- `pkg/addon/controllers/` - Add-on lifecycle controllers

**Agents**:
- `pkg/registration/spoke/` - Registration agent for managed clusters
- `pkg/work/spoke/` - Work execution agent
- `pkg/addon/templateagent/` - Template-based add-on agents

**APIs**: CRD definitions are maintained in a separate `api` repository and copied via `make copy-crd`

## Development Workflow

**API Changes**: If modifying CRDs, changes must be made in the separate `api` repository first, then synced with `make copy-crd`

**Local Development**:
1. Use `go mod replace` directives for local API development
2. Run `make update` after API changes
3. Test with kind clusters using setup scripts in `solutions/setup-dev-environment/`

**Code Standards**:
- DCO sign-off required on all commits
- Import organization enforced via `gci` tool
- golangci-lint with comprehensive rules
- OpenShift build machinery conventions

**Common Debug Points**:
- Controller logs: Check reconciliation loops in hub controllers
- Agent connectivity: Verify mTLS certificates and network policies
- ManifestWork status: Check work agent logs on managed clusters
- Placement decisions: Review placement controller logic and cluster scores

## Build Output

Built components create these key executables:
- `registration` - Hub registration controller + spoke agent
- `work` - Hub work controller + spoke work agent
- `placement` - Placement controller with scheduling plugins
- `registration-operator` - Operator for managing OCM lifecycle
- `addon-manager` - Add-on framework manager