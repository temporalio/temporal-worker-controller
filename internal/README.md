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

3. Install the Helm chart:
   ```bash
   cd helm/temporal-worker-controller
   helm install temporal-worker-controller . --namespace temporal-system
   ```

4. Deploy a sample TemporalWorkerDeployment and TemporalConnection:
   ```bash
   skaffold dev --profile helloworld
   ```

5. Watch the deployment status:
   ```bash
   watch kubectl get twd
   ```

### Understanding the Demo

The demo will:
1. Create a TemporalConnection to your Temporal Cloud namespace
2. Deploy a TemporalWorkerDeployment that runs a sample worker
3. The controller will manage the worker's lifecycle in Kubernetes

You can monitor the controller's logs and the worker's status using:
```bash
# View controller logs
kubectl logs -n temporal-system deployment/temporal-worker-controller

# View worker deployment status
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