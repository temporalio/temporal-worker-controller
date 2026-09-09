# Upgrading Temporal Worker Controller

## Overview

The Temporal Worker Controller ships as **two Helm charts**:

`temporal-worker-controller-crds` | Custom Resource Definitions (CRDs) 
`temporal-worker-controller` | Controller deployment, RBAC, webhooks 

**Always upgrade the CRD chart first, then the controller chart.** See crd-management.md for the full rationale and compatibility commitment.

## Before You Upgrade

Check your currently installed versions:

```bash
helm list -n temporal-system
```

Review the [compatibility commitment](crd-management.md#compatibility-commitment)

## Upgrade Procedure

```bash
# 1. Upgrade CRDs first
helm upgrade temporal-worker-controller-crds \
  oci://ghcr.io/temporalio/helm-charts/temporal-worker-controller-crds \
  --version <target-version> \
  --namespace temporal-system

# 2. Then upgrade the controller
helm upgrade temporal-worker-controller \
  oci://ghcr.io/temporalio/helm-charts/temporal-worker-controller \
  --version <target-version> \
  --namespace temporal-system
```

See [CRD Management — Upgrading](crd-management.md#upgrading) for detailed instructions and rollback procedures.

## Webhook and TLS Configuration

### What the webhooks do

TWC runs two admission webhooks:
**WorkerResourceTemplate** validator | Validates WRT create/update/delete. Security control with `failurePolicy: Fail`  if the webhook is down, all WRT operations are blocked cluster-wide. | Always on |
**WorkerDeployment** validator | Validates and defaults WD create/update. | `webhook.enabled` (default: `false`) |

### Why a TLS certificate is always required

Kubernetes requires all webhooks to serve HTTPS. Because the WRT webhook is always on, the controller pod **always** needs a TLS certificate - regardless of the `webhook.enabled` setting. The certificate is mounted from a Kubernetes Secret into the pod.

### Certificate options

**Option 1: cert-manager (default)**

With `certmanager.enabled: true` (the default), the chart creates an Issuer and Certificate resource. cert-manager generates the TLS certificate and stores it in a Secret. Cert-manager must be installed independently in the cluster before installing TWC.

```bash
# Install cert-manager (once per cluster)
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set crds.enabled=true

# Install TWC (cert-manager creates the webhook cert automatically)
helm install temporal-worker-controller <chart> \
  --namespace temporal-system \
  --set certmanager.enabled=true
```

**Option 2: Bring your own certificate**

If you manage TLS certificates outside of cert-manager, create the Secret yourself and tell TWC its name:

```bash
# Create your TLS Secret
kubectl create secret tls my-webhook-cert \
  --cert=webhook.pem \
  --key=webhook-key.pem \
  --namespace temporal-system

# Install TWC pointing at your Secret
helm install temporal-worker-controller <chart> \
  --namespace temporal-system \
  --set certmanager.enabled=false \
  --set webhook.certSecretName=my-webhook-cert \
  --set certmanager.caBundle=$(base64 < ca.pem)
```

The `webhook.certSecretName` value (default: `webhook-server-cert`) controls which Secret the controller pod mounts for TLS. The `caBundle` value tells the Kubernetes API server which CA to trust when calling the webhook.

**Option 3: Self-managed with cert-manager**

If you use cert-manager but want to control the Secret name:

```bash
helm install temporal-worker-controller <chart> \
  --namespace temporal-system \
  --set certmanager.enabled=true \
  --set webhook.certSecretName=my-custom-cert-name
```

## Migrating from the cert-manager Subchart

### Who is affected

Only users who previously set `certmanager.install: true` in their TWC Helm values. This installed cert-manager **as a subchart** inside TWC's Helm release. The default was `false`

If you are unsure if you're effected, check:

```bash
helm get values temporal-worker-controller -n temporal-system | grep "certmanager"
```

If the output does not show `install: true`, you can skip this section.

### Why migration is required

When cert-manager is installed as a TWC subchart, Helm treats all cert-manager resources as part of the TWC release.
Removing the subchart dependency (upgrading to a version without it) would cause Helm to **delete** those cert-manager resources and removing cert-manager from the cluster entirely. This migration prevents that.

### Migration steps

The following steps have been tested on both Helm 3.21.4 and Helm 4.2.3.

#### Step 1: Annotate cert-manager resources

Add the `helm.sh/resource-policy: keep` annotation to every cert-manager resource owned by the TWC release. This tells Helm to preserve them during the upgrade.

```bash
# ServiceAccounts
for sa in temporal-worker-controller-cert-manager-cainjector \
          temporal-worker-controller-cert-manager \
          temporal-worker-controller-cert-manager-webhook; do
  kubectl annotate serviceaccount $sa -n <namespace> helm.sh/resource-policy=keep
done

# Deployments
for deploy in temporal-worker-controller-cert-manager \
              temporal-worker-controller-cert-manager-cainjector \
              temporal-worker-controller-cert-manager-webhook; do
  kubectl annotate deployment $deploy -n <namespace> helm.sh/resource-policy=keep
done

# Services
for svc in temporal-worker-controller-cert-manager \
            temporal-worker-controller-cert-manager-webhook; do
  kubectl annotate service $svc -n <namespace> helm.sh/resource-policy=keep
done

# Roles and RoleBindings (names may vary by cert-manager version)
kubectl annotate role temporal-worker-controller-cert-manager-webhook:dynamic-serving \
  -n <namespace> helm.sh/resource-policy=keep
kubectl annotate rolebinding temporal-worker-controller-cert-manager-webhook:dynamic-serving \
  -n <namespace> helm.sh/resource-policy=keep

# CRDs
for crd in challenges.acme.cert-manager.io \
           orders.acme.cert-manager.io \
           certificaterequests.cert-manager.io \
           certificates.cert-manager.io \
           clusterissuers.cert-manager.io \
           issuers.cert-manager.io; do
  kubectl annotate crd $crd helm.sh/resource-policy=keep
done

# ClusterRoles
for cr in temporal-worker-controller-cert-manager-cainjector \
          temporal-worker-controller-cert-manager-controller-issuers \
          temporal-worker-controller-cert-manager-controller-clusterissuers \
          temporal-worker-controller-cert-manager-controller-certificates \
          temporal-worker-controller-cert-manager-controller-orders \
          temporal-worker-controller-cert-manager-controller-challenges \
          temporal-worker-controller-cert-manager-controller-ingress-shim \
          temporal-worker-controller-cert-manager-cluster-view \
          temporal-worker-controller-cert-manager-view \
          temporal-worker-controller-cert-manager-edit \
          temporal-worker-controller-cert-manager-controller-approve:cert-manager-io \
          temporal-worker-controller-cert-manager-controller-certificatesigningrequests \
          temporal-worker-controller-cert-manager-webhook:subjectaccessreviews; do
  kubectl annotate clusterrole $cr helm.sh/resource-policy=keep
done

# ClusterRoleBindings
for crb in temporal-worker-controller-cert-manager-cainjector \
           temporal-worker-controller-cert-manager-controller-issuers \
           temporal-worker-controller-cert-manager-controller-clusterissuers \
           temporal-worker-controller-cert-manager-controller-certificates \
           temporal-worker-controller-cert-manager-controller-orders \
           temporal-worker-controller-cert-manager-controller-challenges \
           temporal-worker-controller-cert-manager-controller-ingress-shim \
           temporal-worker-controller-cert-manager-controller-approve:cert-manager-io \
           temporal-worker-controller-cert-manager-controller-certificatesigningrequests \
           temporal-worker-controller-cert-manager-webhook:subjectaccessreviews; do
  kubectl annotate clusterrolebinding $crb helm.sh/resource-policy=keep
done

# Webhook configurations
kubectl annotate mutatingwebhookconfiguration \
  temporal-worker-controller-cert-manager-webhook helm.sh/resource-policy=keep
kubectl annotate validatingwebhookconfiguration \
  temporal-worker-controller-cert-manager-webhook helm.sh/resource-policy=keep
```

> **Note:** Some resources (e.g. leaderelection Roles) may not exist depending on your cert-manager version. "Not found" errors for those are safe to ignore.

Replace `<namespace>` with the namespace where TWC is installed.

#### Step 2: Upgrade TWC with the subchart disabled

```bash
helm upgrade temporal-worker-controller <chart> \
  --namespace temporal-system \
  --set certmanager.install=false \
  --set certmanager.enabled=true
```

Verify cert-manager resources survived:

```bash
# cert-manager pods should still be running
kubectl get pods -n temporal-system | grep cert-manager

# Webhook cert Secret should still exist
kubectl get secret webhook-server-cert -n temporal-system

# TWC manager should be healthy
kubectl get pods -n temporal-system | grep manager
```

#### Step 3: Transfer CRD ownership

The cert-manager CRDs still carry ownership labels pointing to the TWC release. The independent cert-manager install needs to adopt them:

```bash
for crd in challenges.acme.cert-manager.io \
           orders.acme.cert-manager.io \
           certificaterequests.cert-manager.io \
           certificates.cert-manager.io \
           clusterissuers.cert-manager.io \
           issuers.cert-manager.io; do
  kubectl annotate crd $crd meta.helm.sh/release-name=cert-manager --overwrite
  kubectl annotate crd $crd meta.helm.sh/release-namespace=cert-manager --overwrite
done
```

#### Step 4: Transfer Certificate and Issuer ownership

> **Important:** Without this step, the new cert-manager instance will not renew the webhook certificate when it expires. The existing certificate continues to work until expiry, but renewal will silently fail.

```bash
kubectl annotate certificate temporal-worker-controller-serving-cert \
  -n temporal-system meta.helm.sh/release-name=cert-manager --overwrite
kubectl annotate certificate temporal-worker-controller-serving-cert \
  -n temporal-system meta.helm.sh/release-namespace=cert-manager --overwrite

kubectl annotate issuer temporal-worker-controller-selfsigned-issuer \
  -n temporal-system meta.helm.sh/release-name=cert-manager --overwrite
kubectl annotate issuer temporal-worker-controller-selfsigned-issuer \
  -n temporal-system meta.helm.sh/release-namespace=cert-manager --overwrite
```

#### Step 5: Clean up old cert-manager resources

Remove the old subchart's cert-manager resources from the TWC namespace. This must be done **before** installing cert-manager independently to avoid RBAC conflicts.

```bash
# Deployments
kubectl delete deployment \
  temporal-worker-controller-cert-manager \
  temporal-worker-controller-cert-manager-cainjector \
  temporal-worker-controller-cert-manager-webhook \
  -n temporal-system

# Services
kubectl delete service \
  temporal-worker-controller-cert-manager \
  temporal-worker-controller-cert-manager-webhook \
  -n temporal-system

# ServiceAccounts
kubectl delete serviceaccount \
  temporal-worker-controller-cert-manager \
  temporal-worker-controller-cert-manager-cainjector \
  temporal-worker-controller-cert-manager-webhook \
  -n temporal-system

# Roles and RoleBindings
kubectl delete role temporal-worker-controller-cert-manager-webhook:dynamic-serving \
  -n temporal-system
kubectl delete rolebinding temporal-worker-controller-cert-manager-webhook:dynamic-serving \
  -n temporal-system

# ClusterRoles and ClusterRoleBindings
kubectl delete clusterrole \
  -l app.kubernetes.io/instance=temporal-worker-controller \
  -l app.kubernetes.io/name=cert-manager
kubectl delete clusterrolebinding \
  -l app.kubernetes.io/instance=temporal-worker-controller \
  -l app.kubernetes.io/name=cert-manager

# Webhook configurations
kubectl delete mutatingwebhookconfiguration \
  temporal-worker-controller-cert-manager-webhook
kubectl delete validatingwebhookconfiguration \
  temporal-worker-controller-cert-manager-webhook
```

#### Step 6: Install cert-manager independently

```bash
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set crds.enabled=true \
  --wait
```

#### Step 7: Verify

```bash
# New cert-manager is running
kubectl get pods -n cert-manager

# Old cert-manager is gone from the TWC namespace
kubectl get pods -n temporal-system | grep cert-manager
# (should return nothing)

# Webhook cert Secret was recreated by the new cert-manager
kubectl get secret webhook-server-cert -n temporal-system

# TWC controller is healthy
kubectl get pods -n temporal-system | grep manager

# New cert-manager can renew the certificate
kubectl logs -n cert-manager -l app.kubernetes.io/name=cert-manager --tail=20 \
  | grep -i "temporal\|issuing\|serving-cert"
```

## Version-Specific Notes

> **Upgrading from v1.4.0 to v1.5.2+**
>
> In v1.4.0, the webhook certificate volume was only mounted when `webhook.enabled: true`. Starting in v1.5.2, the volume mount became unconditional (the always-on WRT webhook needs TLS). If you are upgrading from v1.4.0 with `certmanager.enabled: false`, you must create the webhook TLS Secret before upgrading or the controller pod will be stuck in `ContainerCreating`.

> **v1.5.2 through v1.7.0: `webhook.enabled` had no effect**
>
> In these versions, the `webhook.enabled` value was documented as controlling the WorkerDeployment validating webhook, but that webhook was not actually wired up in the controller. Setting `webhook.enabled: true` only installed the webhook Service with no backing handler. This was fixed in v1.8.0.

> **v1.8.0+: WorkerDeployment webhook fully operational**
>
> Starting in v1.8.0, the WorkerDeployment validating and defaulting webhook is fully wired up. Setting `webhook.enabled: true` now correctly enables WorkerDeployment admission validation.

## Common Issues

### Pod stuck in `ContainerCreating`

**Symptom:** The controller pod stays in `ContainerCreating` with the event:

```
MountVolume.SetUp failed for volume "cert": secret "webhook-server-cert" not found
```

**Cause:** The chart always mounts the webhook TLS certificate Secret (the WRT webhook needs it). If cert-manager is not running and you haven't created the Secret manually, the mount fails.

**Fix:** Either enable cert-manager (`certmanager.enabled: true` with cert-manager installed in the cluster) or create the Secret yourself:

```bash
kubectl create secret tls webhook-server-cert \
  --cert=<path-to-cert> \
  --key=<path-to-key> \
  --namespace temporal-system
```

Or if using a different Secret name, set `webhook.certSecretName` accordingly.

### WRT operations blocked cluster-wide

**Symptom:** Creating, updating, or deleting WorkerResourceTemplates fails with a webhook error.

**Cause:** The WRT validating webhook uses `failurePolicy: Fail`. If the controller pod is not running or the webhook TLS certificate is invalid/expired, the API server cannot reach the webhook and rejects all WRT operations.

**Fix:** Ensure the controller pods are running and the TLS certificate is valid:

```bash
kubectl get pods -n temporal-system | grep manager
kubectl get secret webhook-server-cert -n temporal-system
kubectl get certificate -n temporal-system
```

### cert-manager CRD conflicts during installation

**Symptom:** `helm install cert-manager` fails with:

```
CustomResourceDefinition "certificates.cert-manager.io" exists and cannot be imported:
invalid ownership metadata
```

**Cause:** The CRDs still carry ownership labels from a previous Helm release (e.g. the TWC subchart release). Helm refuses to adopt resources owned by another release.

**Fix:** Transfer CRD ownership to the new release:

```bash
for crd in challenges.acme.cert-manager.io orders.acme.cert-manager.io \
           certificaterequests.cert-manager.io certificates.cert-manager.io \
           clusterissuers.cert-manager.io issuers.cert-manager.io; do
  kubectl annotate crd $crd meta.helm.sh/release-name=cert-manager --overwrite
  kubectl annotate crd $crd meta.helm.sh/release-namespace=cert-manager --overwrite
done
```

### Certificate not renewing after migration

**Symptom:** The webhook works now but cert-manager logs show:

```
Not syncing resource as it is not owned by this controller
```

**Cause:** The Certificate and Issuer resources still carry ownership labels from the old TWC release. The new cert-manager instance ignores them.

**Fix:** Transfer ownership of the Certificate and Issuer (see [Step 4](#step-4-transfer-certificate-and-issuer-ownership) of the migration guide).

## Rollback

See [CRD Management — Rollback](crd-management.md#rollback) for rollback procedures. When rolling back, downgrade the controller chart first, then the CRD chart.