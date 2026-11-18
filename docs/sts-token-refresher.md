**AWS STS Token Refresher**

The `sts-token-refresher` command obtains a short-lived AWS identity token and writes it to the API-key Secret referenced by a `ClusterConnection`. Run it as an extra container in the controller pod, using the same controller image.

**Requirements**

- Enable AWS outbound identity federation in the account and configure the Temporal server to trust that account's issuer.
- Give the controller ServiceAccount an AWS identity through EKS Pod Identity or IRSA. Its role needs `sts:GetWebIdentityToken` for the configured audience, signing algorithm, and maximum token lifetime.
- Use the regional AWS STS endpoint. `GetWebIdentityToken` is not available on the global STS endpoint.
- Keep `rbac.restrictWatchNamespaces` empty. The refresher lists `WorkerDeployment` resources cluster-wide, and `ClusterConnection` references require cluster-wide controller mode.
- Pre-create a Secret named `temporal-api-key` with type `Opaque` in every namespace that has a `WorkerDeployment` referencing this connection. The refresher writes the `api-key` key, so leave that key out of the GitOps desired data.

The refresher updates the Secret within one minute of token expiry and syncs WorkerDeployment namespaces every 30 seconds by default. The controller reads the API-key Secret while making Temporal requests, so it can use a refreshed token without restarting.

The controller passes the same API key to worker pods as `TEMPORAL_API_KEY`. A worker that uses its own credentials must pass those credentials explicitly to its Temporal SDK.

**Container arguments**

Pass `sts-token-refresher` as the first argument, followed by:

| Argument | Purpose |
| --- | --- |
| `--audience` | Audience configured in Temporal and the IAM policy. |
| `--connection-name` | Name of the `ClusterConnection` whose WorkerDeployment namespaces to sync. |
| `--region` | Regional AWS STS endpoint. Defaults to `AWS_REGION`. |
| `--sync-interval` | Namespace refresh interval. Defaults to `30s`. |

The sidecar needs `POD_NAME` and `POD_NAMESPACE` environment variables from the Pod fields for leader election. It shares the controller pod's ServiceAccount and AWS role.
