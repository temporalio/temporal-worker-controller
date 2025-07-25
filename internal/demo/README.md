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

### Available Rollout Strategies

The controller supports three deployment strategies:

🚀 **ALL-AT-ONCE**: Immediately routes all new workflows to the latest version
- Ideal for initial deployments when no traffic exists
- Faster deployment, appropriate when risk is minimal
- Used by default in our demo for the unversioned → v1 transition

📈 **PROGRESSIVE**: Gradually shifts traffic using defined percentage steps
- Best for production with existing traffic: minimizes risk by allowing observation at each step
- Configured with `rampPercentage` and `pauseDuration` steps:
  - `rampPercentage`: What percentage of new workflows go to the new version (e.g., 10% to v2, 90% to v1)
  - `pauseDuration`: How long to wait at each percentage before moving to the next step
- Demonstrated in our demo for the v1 → v2 transition with live traffic

⚙️ **MANUAL**: Requires operator intervention to control version ramping
- Maximum control for critical deployments
- Use [Temporal CLI commands](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning#rolling-out-changes-with-the-cli) to manually promote versions

### Testing Deployment Strategies

#### 🚀 **ALL-AT-ONCE** Strategy: Unversioned → v1

4. **Deploy the v1 worker** using the **All-At-Once rollout strategy**:
   ```bash
   skaffold dev --profile helloworld-worker
   ```
   This deploys a TemporalWorkerDeployment and TemporalConnection Custom Resource using the **All-At-Once strategy**. Since there's no existing traffic, v1 immediately becomes the current version for this worker deployment.
   This would mean that all new workflow executions, that are scheduled to run on workers part of this version, will start on v1.
   
5. Watch the deployment status:
   ```bash
   watch kubectl get twd
   ```

6. **Apply load** to the v1 worker to simulate production traffic:
    ```bash
    make apply-load-sample-workflow
    ```

#### 📈 **PROGRESSIVE** Strategy: v1 → v2 (Non-Replay-Safe Change)

7. **Switch to the Progressive rollout strategy** for safer deployments:
   ```bash
   git apply internal/demo/helloworld/changes/progressive-rollout.patch
   ```
   This changes the strategy from **All-At-Once** to **Progressive** with ramping steps (1% → 5% → 10% → 50% → 100%).

8. **Deploy a non-replay-safe workflow change**:
   ```bash
   git apply internal/demo/helloworld/changes/no-version-gate.patch
   ```
   This applies a **non-replay-safe change** (switching from custom Sleep activity to built-in `workflow.Sleep`). 
   Skaffold automatically detects the change and deploys worker v2.

9. **Observe the progressive rollout managing incompatible versions**:
   - New workflow executions gradually shift from v1 to v2 following the rollout steps (1% → 5% → 10% → 50% → 100%)
   - **Both worker versions run simultaneously** - this is critical since the code changes are incompatible
   - v1 workers continue serving existing workflows (which would fail to replay on v2)
   - v2 workers handle new workflow executions which has the updated code
   - This demonstrates how **Progressive rollout** safely handles breaking changes when you have existing traffic

### Monitoring 

You can monitor the controller's logs and the worker's status using:
```bash
# Fetch the controller pod name
minikube kubectl -- get pods -n temporal-worker-controller -w

# Describe the controller pod's status
minikube kubectl -- describe pod <pod-name> -n temporal-worker-controller

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

