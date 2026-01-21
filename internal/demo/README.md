# Local Development Setup

This guide will help you set up and run the Temporal Worker Controller locally using Minikube.

### Prerequisites

- [Minikube](https://minikube.sigs.k8s.io/docs/start/)
- [Helm](https://helm.sh/docs/intro/install/)
- [Skaffold](https://skaffold.dev/docs/install/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- Temporal Cloud account with API key or mTLS certificates
- Understanding of [Worker Versioning concepts](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) (Pinned and Auto-Upgrade versioning behaviors)

> **Note**: This demo specifically showcases **Pinned** workflow behavior. All workflows in the demo will remain on the worker version where they started, demonstrating how the controller safely manages multiple worker versions simultaneously during deployments.

### Running the Local Demo

1. Start a local Minikube cluster:
   ```bash
   minikube start --nodes 2
   ```

2. Create the `skaffold.env` file:
   - Run:
     ```bash
     cp skaffold.example.env skaffold.env
     ```

   - Update the value of `TEMPORAL_NAMESPACE`, `TEMPORAL_ADDRESS`  in `skaffold.env` to match your configuration.

2. Set up Temporal Cloud Authentication:
   - Create a `certs` directory in the project root
   - Save your Temporal Cloud mTLS client certificates as:
     - `certs/client.pem`
     - `certs/client.key`
   - Create the Kubernetes secret:
     ```bash
     make create-cloud-mtls-secret
     ```
   - In `skaffold.env`, set:
     ```env
     TEMPORAL_API_KEY_SECRET_NAME=""
     TEMPORAL_MTLS_SECRET_NAME=temporal-cloud-mtls-secret
     ```

   NOTE: Alternatively, if you are using API keys, follow the steps below instead of mTLS:

   #### Using API Keys (alternative to mTLS)
   - Create a `certs` directory in the project root if not already present
   - Save your Temporal Cloud API key in a file (single line, no newline):
     ```bash
     echo -n "<YOUR_API_KEY>" > certs/api-key.txt
     ```
   - Create the Kubernetes Secret:
     ```bash
     make create-api-key-secret
     ```
   - In `skaffold.env`, set:
     ```env
     TEMPORAL_API_KEY_SECRET_NAME=temporal-api-key
     TEMPORAL_MTLS_SECRET_NAME=""
     ```
   - Note: Do not set both mTLS and API key for the same connection. If both present, the TemporalConnection Custom Resource
   Instance will not get installed in the k8s environment.

4. Build and deploy the Controller image to the local k8s cluster:
   ```bash
   skaffold run --profile worker-controller
   ```

### Testing Progressive Deployments

5. **Deploy the v1 worker**:
   ```bash
   skaffold run --profile helloworld-worker
   ```
   This deploys a TemporalWorkerDeployment and TemporalConnection Custom Resource using the **Progressive strategy**. Note that when there is no current version (as in an initial versioned worker deployment), the progressive steps are skipped and v1 becomes the current version immediately. All new workflow executions will now start on v1.
   
6. Watch the deployment status:
   ```bash
   watch kubectl get twd
   ```

7. **Apply load** to the v1 worker to simulate production traffic:
    ```bash
    make apply-load-sample-workflow
    ```

#### **Progressive Rollout of v2** (Non-Replay-Safe Change)

8. **Deploy a non-replay-safe workflow change**:
   ```bash
   git apply internal/demo/helloworld/changes/no-version-gate.patch
   skaffold run --profile helloworld-worker
   ```
   This applies a **non-replay-safe change** (switching an activity response type from string to a struct).

9. **Observe the progressive rollout managing incompatible versions**:
   - New workflow executions gradually shift from v1 to v2 following the configured rollout steps (1% → 5% → 10% → 50% → 100%)
   - **Both worker versions run simultaneously** - this is critical since the code changes are incompatible
   - v1 workers continue serving existing workflows (which would fail to replay on v2)
   - v2 workers handle new workflow executions with the updated code
   - This demonstrates how **Progressive rollout** safely handles breaking changes when you have existing traffic

### Monitoring 

You can monitor the controller's logs and the worker's status using:
```bash
# Output the controller pod's logs
kubectl logs -n temporal-system deployments/temporal-worker-controller-manager -f

# View TemporalWorkerDeployment status
kubectl get twd
```

### Cleanup

To clean up the demo:
```bash
# Delete the Helm release
helm uninstall temporal-worker-controller -n temporal-system

# Stop Minikube
minikube stop
```

### Additional Operational commands

Complete cleanup (removes all clusters, cached images, and config):
```
minikube delete --all --purge
```

**What `minikube delete --all --purge` does:**
- `--all`: Deletes ALL minikube clusters (not just the default one)
- `--purge`: Completely removes all minikube data, cached images, and configuration files from your machine

This gives you a completely fresh start and frees up disk space used by minikube. 
