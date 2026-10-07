# ClusterConnection

A `ClusterConnection` is the cluster-scoped version of a [`Connection`](configuration.md#connection-configuration). It has the same `spec`. The difference is that a `WorkerDeployment` in **any** namespace can reference it, so you define the Temporal server details once for the whole cluster instead of once per namespace.

`ClusterConnection` is available from Temporal Worker Controller v1.10.0.

## When to use it

Use a `ClusterConnection` when many namespaces connect to the same Temporal server in the same way. For example, one namespace per team or per worker, or short-lived per-PR namespaces, all pointing at the same Temporal Cloud endpoint.

Use a namespaced `Connection` when:
- each namespace needs different connection settings, or
- the people who manage a namespace should also manage its connection.

A `ClusterConnection` is a cluster-wide object, so it is owned by a cluster admin. The chart's optional end-user roles (`rbac.createEndUserRoles`) cover `connections` only, not `clusterconnections`.

## Requirements

The controller must run cluster-wide, which is the default. If you set `rbac.restrictWatchNamespaces`, the controller runs in namespace-scoped mode and cannot read cluster-scoped resources. A `WorkerDeployment` that references a `ClusterConnection` then stays blocked:
- `Progressing=False` and `Ready=False`, with reason `ClusterConnectionUnsupported`

## Example

Create the `ClusterConnection`. It has no `metadata.namespace`:

```yaml
apiVersion: temporal.io/v1alpha1
kind: ClusterConnection
metadata:
  name: production-temporal
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  apiKeySecretRef:
    name: temporal-api-key  # Looked up in each WorkerDeployment's namespace, see below
    key: api-key
```

In each `WorkerDeployment`, reference it with `connectionRef.objectRef`:

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: my-worker
  namespace: team-a
spec:
  workerOptions:
    connectionRef:
      objectRef:
        apiGroup: temporal.io
        kind: ClusterConnection
        name: production-temporal
    temporalNamespace: production
  # ...
```

`connectionRef` takes exactly one of two forms:
- `connectionRef.name` is shorthand for a namespaced `Connection` in the `WorkerDeployment`'s own namespace.
- `connectionRef.objectRef` can point at either a `Connection` or a `ClusterConnection`. `apiGroup` must be `temporal.io`, and `kind` must be `Connection` or `ClusterConnection`.

## Where the secrets live

A `ClusterConnection` has no namespace, but the Secrets it references to. **Every Secret named in a `ClusterConnection` is looked up in the namespace of the `WorkerDeployment` that uses it.** That covers `mutualTLSSecretRef`, `apiKeySecretRef` and `tls.caCertSecretRef`.

So for the example above, every namespace with a `WorkerDeployment` that uses `production-temporal` needs its own `temporal-api-key` Secret with an `api-key` key.

The reason is that the same Secrets are passed to the worker Pods:
- the API key as the `TEMPORAL_API_KEY` environment variable,
- the mTLS certificate and the CA certificate as mounted volumes.

Kubernetes only lets a Pod use Secrets from its own namespace. Keeping a single copy of the Secret, for example in the controller's namespace, is not supported yet. [#578](https://github.com/temporalio/temporal-worker-controller/issues/578) tracks giving the controller its own credentials, separate from the worker Pods.

## How changes are applied

- **Changing the `ClusterConnection` spec** affects every `WorkerDeployment` that references it, in every namespace. Each one rolls its current and target versions onto the new settings. As with a [`connectionRef` change](concepts.md#worker-options), Draining and Drained versions keep the connection they were created with.
- **Deleting a `ClusterConnection`** is blocked by a finalizer while any `WorkerDeployment` in any namespace still references it. The controller removes the finalizer once the last referencing `WorkerDeployment` is deleted or moved to another connection.
- **Names are scoped by kind.** A namespaced `Connection` and a `ClusterConnection` with the same name are different objects. Switching a `WorkerDeployment` between them is an ordinary `connectionRef` change.