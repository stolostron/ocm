# cluster-manager

The cluster-manager provides the multicluster hub, which can manage Kubernetes-based clusters across data centers,
public clouds, and private clouds. This operator supports the installation and upgrade of ClusterManager.

## Get Repo Info

```bash
helm repo add ocm https://open-cluster-management.io/helm-charts
helm repo update
helm search repo ocm
```

## Install the Chart

For example, install the chart into `open-cluster-management` namespace.

```bash
helm install cluster-manager  --version <version> ocm/cluster-manager --namespace=open-cluster-management --create-namespace
```

## Uninstall

### Delete all managedClusters before uninstall the Chart

```bash
kubectl get managedcluster | awk '{print $1}' | xargs kubectl delete managedcluster
```

### And then delete the clusterManager CR

``` bash
kubectl delete clustermanagers cluster-manager
```

### Uninstall the Chart

```bash
helm uninstall cluster-manager --namespace=open-cluster-management
```

## NetworkPolicies (optional)

Opt-in NetworkPolicies for hub components (disabled by default).

```bash
helm upgrade --install cluster-manager ocm/cluster-manager \
  --namespace open-cluster-management --create-namespace \
  --set networkPolicies.enabled=true
```

Or patch after install:

```bash
kubectl patch clustermanager cluster-manager --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":true}}}'
```

On **MCE / stolostron**, use the CR patch (the product does not use this Helm chart for hub pods). See [NETWORK_POLICIES.md](../../../NETWORK_POLICIES.md) for full details, backward compatibility, and MCE notes.
