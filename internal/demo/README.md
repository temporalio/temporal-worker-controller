# Local Development Setup

This guide will help you set up and run the Temporal Worker Controller locally using Minikube.

### Prerequisites

- [Minikube](https://minikube.sigs.k8s.io/docs/start/)
- [Helm](https://helm.sh/docs/intro/install/)
- [Skaffold](https://skaffold.dev/docs/install/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- Temporal Cloud account with mTLS certificates
- Understanding of [Worker Versioning concepts](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) (Pinned and Auto-Upgrade versioning behaviors)

> **Note**: This demo specifically showcases **Pinned** workflow behavior. All workflows in the demo will remain on the worker version where they started, demonstrating how the controller safely manages multiple worker versions simultaneously during deployments.

### Running the Local Demo

1. Start a local Minikube cluster:
   ```bash
   minikube start
   ```

2. Set up Temporal Cloud mTLS certificates:
   - Create a `certs` directory in the project root
   - Save your Temporal Cloud mTLS client certificates as:
     - `certs/client.pem`
     - `certs/client.key`
   - Create the Kubernetes secret:
     ```bash
     make create-cloud-mtls-secret
     ```

3. Build and deploy the Controller image to the local k8s cluster:
   ```bash
   skaffold dev --profile worker-controller
   ```

### Testing Progressive Deployments

4. **Deploy the v1 worker**:
   ```bash
   skaffold dev --profile helloworld-worker
   ```
   This deploys a TemporalWorkerDeployment and TemporalConnection Custom Resource using the **Progressive strategy**. Note that when there is no current version (as in an initial versioned worker deployment), the progressive steps are skipped and v1 becomes the current version immediately. All new workflow executions will now start on v1.
   
5. Watch the deployment status:
   ```bash
   watch kubectl get twd
   ```

6. **Apply load** to the v1 worker to simulate production traffic:
    ```bash
    make apply-load-sample-workflow
    ```

#### **Progressive Rollout of v2** (Non-Replay-Safe Change)

7. **Deploy a non-replay-safe workflow change**:
   ```bash
   git apply internal/demo/helloworld/changes/no-version-gate.patch
   ```
   This applies a **non-replay-safe change** (switching from custom Sleep activity to built-in `workflow.Sleep`). 
   Skaffold automatically detects the change and deploys worker v2.

8. **Observe the progressive rollout managing incompatible versions**:
   - New workflow executions gradually shift from v1 to v2 following the configured rollout steps (1% → 5% → 10% → 50% → 100%)
   - **Both worker versions run simultaneously** - this is critical since the code changes are incompatible
   - v1 workers continue serving existing workflows (which would fail to replay on v2)
   - v2 workers handle new workflow executions with the updated code
   - This demonstrates how **Progressive rollout** safely handles breaking changes when you have existing traffic

### Monitoring 

You can monitor the controller's logs and the worker's status using:
```bash
# Fetch the controller pod name
minikube kubectl -- get pods -n temporal-worker-controller -w

# Describe the controller pod's status
minikube kubectl -- describe pod <pod-name> -n temporal-worker-controller

# Output the controller pod's logs
minikube kubectl -- logs -n temporal-system -f pod/<pod-name>

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

