# Local Development Setup

This guide will help you set up and run the Temporal Worker Controller locally using Minikube.

### Prerequisites

- [Minikube](https://minikube.sigs.k8s.io/docs/start/)
- [Helm](https://helm.sh/docs/intro/install/)
- [Skaffold](https://skaffold.dev/docs/install/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- Temporal Cloud account with mTLS certificates

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

4. Deploy a sample TemporalWorkerDeployment and TemporalConnection:
   ```bash
   skaffold dev --profile helloworld-worker
   ```

5. Watch the deployment status:
   ```bash
   watch kubectl get twd
   ```

### Understanding the Demo

TODO (Shivam): complete this section so that we also speak about what happens after we introduce some patch work into it.





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

