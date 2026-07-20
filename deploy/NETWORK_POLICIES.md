# NetworkPolicies (opt-in)

Operator-managed NetworkPolicies for hub and spoke components. **Disabled by default.**

When enabled, the registration-operator applies a simple allow-list NetworkPolicy for selected pods (deny all ingress; allow DNS and Kubernetes API egress). When disabled, those NetworkPolicies are removed.

| CR | Field | Example policy applied today |
|----|-------|------------------------------|
| `ClusterManager` | `spec.networkPolicies.enabled` | Placement controller (`app=clustermanager-placement-controller`) in the hub namespace (typically `open-cluster-management-hub`) |
| `Klusterlet` | `spec.networkPolicies.enabled` | Singleton agent (`app=klusterlet-agent`) in the agent namespace (typically `open-cluster-management-agent`) |

This is an initial toggle and example policy set. More component policies can be added behind the same flag later.

---

## Backward compatibility

Adding `spec.networkPolicies` is **backward compatible**:

- The field is **optional**. Existing `ClusterManager` / `Klusterlet` objects without it keep working.
- When the field is unset or `enabled: false`, **no NetworkPolicies are applied** (same behavior as before this feature).
- Older operators that do not know the field ignore it; no policies are created.

**Upgrade order:** install or update the **CRD before or with** the new operator.

| Rollout order | Result |
|---------------|--------|
| New CRD + new operator | Toggle works as documented |
| New CRD + old operator | Field is stored; old operator ignores it (no policies) — safe |
| Old CRD + new operator | Setting `networkPolicies.enabled: true` may be **pruned** by the apiserver; the toggle will not stick until the CRD is updated |

Helm charts always render `networkPolicies.enabled: true|false` into the CR. That is harmless with the new CRD; with an old CRD the field is dropped until the CRD is upgraded.

---

## Toggle via cluster-manager / klusterlet (upstream Helm)

### Install or upgrade with Helm

```bash
# Hub
helm upgrade --install cluster-manager ocm/cluster-manager \
  --namespace open-cluster-management --create-namespace \
  --set networkPolicies.enabled=true

# Spoke
helm upgrade --install klusterlet ocm/klusterlet \
  --namespace open-cluster-management --create-namespace \
  --set networkPolicies.enabled=true \
  --set klusterlet.clusterName=<cluster-name> \
  --set-file bootstrapHubKubeConfig=<path-to-bootstrap-kubeconfig>
```

Or in values:

```yaml
networkPolicies:
  enabled: true
```

Chart values map to:

```yaml
# ClusterManager / Klusterlet
spec:
  networkPolicies:
    enabled: true
```

See also:

- [cluster-manager chart README](cluster-manager/chart/cluster-manager/README.md)
- [klusterlet chart README](klusterlet/chart/klusterlet/README.md)

### Patch the CR after install

```bash
# Hub
kubectl patch clustermanager cluster-manager --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":true}}}'

# Spoke (replace <klusterlet-name>, often "klusterlet")
kubectl patch klusterlet <klusterlet-name> --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":true}}}'
```

### Disable

Set `enabled: false` (Helm `--set` / values, or patch). The operator deletes the NetworkPolicies it created.

```bash
kubectl patch clustermanager cluster-manager --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":false}}}'
```

### Verify

```bash
# Hub placement policy
kubectl get networkpolicy -n open-cluster-management-hub

# Spoke agent policy (Singleton mode)
kubectl get networkpolicy -n open-cluster-management-agent
```

---

## Toggle via MCE / stolostron

On Multicluster Engine (MCE) / ACM, hub components are **not** installed with the upstream `ocm/cluster-manager` Helm chart.

Typical flow:

1. **backplane-operator** deploys the registration-operator (`cluster-manager` Deployment) from its `cluster-manager` toggle chart (usually in `multicluster-engine`).
2. **backplane-operator** creates/updates the **`ClusterManager` CR**.
3. The registration-operator reconciles that CR and applies hub Deployments (and optional NetworkPolicies) under `open-cluster-management-hub`.

Klusterlet on managed clusters is similarly driven by product install / import flows that create a **`Klusterlet` CR**, not by end users running the upstream klusterlet chart in most cases.

### What end users / admins do today

Until **backplane-operator** (or another product installer) sets `spec.networkPolicies.enabled` when building the CR, enable or disable the feature by **patching the CR on the cluster**:

```bash
# On the hub (MCE / ACM hub)
kubectl patch clustermanager cluster-manager --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":true}}}'

# On a managed cluster (or wherever the Klusterlet CR lives)
kubectl patch klusterlet <klusterlet-name> --type=merge -p \
  '{"spec":{"networkPolicies":{"enabled":true}}}'
```

The registration-operator must be a build that includes this feature, and the ClusterManager / Klusterlet **CRDs** must include the `networkPolicies` schema (see [Backward compatibility](#backward-compatibility)).

### Product installer note

Setting Helm `networkPolicies.enabled` on the upstream OCM charts does **not** apply to the MCE install path. A future change in `stolostron/backplane-operator` (for example when constructing the ClusterManager CR in `pkg/foundation`) could expose a MultiClusterEngine / chart value that sets `spec.networkPolicies.enabled` for you. Until that lands, **CR patch** is the supported toggle on MCE/stolostron.

---

## Policy shape (example)

Current policies are intentionally simple:

- **Ingress:** none (deny all ingress to the selected pods)
- **Egress:** UDP/TCP `53` and `5353` (DNS); TCP `443` and `6443` (API)

Peers are port-based (empty `to`/`from`) so the same manifest works across Kubernetes and OpenShift without hard-coding DNS or apiserver namespaces. Tighten peers later if your environment requires it.
