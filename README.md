# Temporal Worker Controller

[![License](https://img.shields.io/github/license/temporalio/temporal-worker-controller)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/temporalio/temporal-worker-controller)](https://goreportcard.com/report/github.com/temporalio/temporal-worker-controller)

> 🚀 **Public Preview**: This project is in [Public Preview](https://docs.temporal.io/evaluate/development-production-features/release-stages) and ready for production use cases*. Core functionality is complete with stable APIs.

**The Temporal Worker Controller makes it simple and safe to deploy Temporal workers on Kubernetes.**

Temporal workflows require deterministic execution, which means updating worker code can break running workflows if the changes aren't backward compatible. Traditional deployment strategies force you to either risk breaking existing workflows or use Temporal's [Patching API](https://docs.temporal.io/patching) to maintain compatibility across versions.

Temporal's [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) feature solves this dilemma by providing programmatic control over worker versions and traffic routing. The Temporal Worker Controller automates a deployment system that uses Worker Versioning on Kubernetes. When you deploy new code, the controller automatically creates a new worker version while keeping the old version running. Existing workflows continue on the old version while new workflows use the new version. This approach eliminates the need for patches in many cases and ensures running workflows are never disrupted.

## What does it do?

🔒 **Protected [Pinned](https://docs.temporal.io/worker-versioning#pinned) workflows** - Workflows pinned to a version stay on that version and won't break  
🎚️ **Controlled rollout for [AutoUpgrade](https://docs.temporal.io/worker-versioning#auto-upgrade) workflows** - AutoUpgrade workflows shifted to new versions with configurable safety controls  
📦 **Automatic version management** - Registers versions with Temporal, manages routing rules, and tracks version lifecycle  
🎯 **Smart traffic routing** - New workflows automatically get routed to your target worker version  
🛡️ **Progressive rollouts** - Catch incompatible changes early with small traffic percentages before they spread  
⚡ **Easy rollbacks** - Instantly route traffic back to a previous version if issues are detected  
📈 **Per-version autoscaling** - Attach HPAs or other custom scalers to each versioned Deployment via [`WorkerResourceTemplate`](docs/worker-resource-templates.md)

## Quick Example

Instead of this traditional approach where deployments can break running workflows:

```yaml
# ❌ Traditional deployment - risky for running workflows
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-worker
spec:
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v2.0.0  # This change might break existing workflows!
```

You define your worker like this:

```yaml
# ✅ Temporal Worker Controller - safe deployments
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
spec:
  replicas: 3
  rollout:
    strategy: Progressive  # Gradual, safe rollout
    steps:
      - rampPercentage: 10
        pauseDuration: 5m
      - rampPercentage: 50
        pauseDuration: 10m
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v2.0.0  # Safe to deploy!
```

When you update the image, the controller automatically:
1. 🆕 Creates a new deployment with your updated worker
2. 📊 Gradually routes new workflows and AutoUpgrade workflows to the new version  
3. 🔒 Keeps Pinned workflows running on their original version (guaranteed safety)
4. 🧹 Automatically scales down and cleans up old versions once they are [drained](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning#sunsetting-an-old-deployment-version)

## 🏃‍♂️ Getting Started

### Prerequisites

- Kubernetes cluster (1.19+)
- Helm [v3.0+](https://github.com/helm/helm/releases) if deploying via our Helm chart
- [Temporal Server](https://docs.temporal.io/) (Cloud or self-hosted [v1.29.1](https://github.com/temporalio/temporal/releases/tag/v1.29.1))
- Basic familiarity with Temporal [Workers](https://docs.temporal.io/workers), [Workflows](https://docs.temporal.io/workflows), and [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning)
- **TLS for the validating webhook** *(required for `WorkerResourceTemplate`)* — the recommended path is [cert-manager](https://cert-manager.io/docs/installation/), which handles certificate provisioning automatically. Install it separately or as a subchart of the controller chart (`certmanager.install: true`). If you prefer to manage TLS yourself, see [Webhook TLS](docs/worker-resource-templates.md#webhook-tls).

### 🔧 Installation

CRDs are shipped as a separate Helm chart so they can be upgraded independently of the controller. Install the CRDs chart first, then the controller chart:

```bash
# 1. Install CRDs
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <version> \
  --namespace <your-namespace> \
  --create-namespace

# 2. Install the controller
helm install temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <version> \
  --namespace <your-namespace>
```

See [docs/crd-management.md](docs/crd-management.md) for upgrade, rollback, and migration instructions.

### Next Steps

**New to deploying workers with this controller?** → Start with our [Migration Guide](docs/migration-to-versioned.md) to learn how to safely transition from traditional deployments.

**Setting up CI/CD for steady-state rollouts?** → See the [CD Rollouts Guide](docs/cd-rollouts.md) for Helm, kubectl, ArgoCD, and Flux integration patterns.

**Ready to dive deeper?** → Check out the [Architecture Guide](docs/architecture.md) to understand how the controller works, or the [Temporal Worker Versioning docs](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) to learn about the underlying Temporal feature.

**Need configuration help?** → See the [Configuration Reference](docs/configuration.md) for all available options.

## Features

- ✅ **Registration of new Temporal Worker Deployment Versions**
- ✅ **Creation of versioned Deployment resources** (managing Pods that run your Temporal workers)
- ✅ **Automatic lifecycle scaling** - Scales down worker versions when no longer needed
- ✅ **Deletion of resources** associated with drained Worker Deployment Versions
- ✅ **Multiple rollout strategies**: `Manual`, `AllAtOnce`, and `Progressive` rollouts
- ✅ **Gate workflows** - Test new versions with a [pre-deployment test](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning#adding-a-pre-deployment-test) before routing real traffic to them
- ✅ **Per-version attached resources** - Attach HPAs, PodDisruptionBudgets, or any namespaced Kubernetes resource to each worker version with running workers via [`WorkerResourceTemplate`](docs/worker-resource-templates.md) — this is also the recommended path for metric-based and backlog-based autoscaling


## 💡 Why Use This?

### Manual Worker Versioning is Complex

While Temporal's [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) feature solves deployment safety problems, using it manually requires:

- **Manual API calls** - Register versions, manage routing rules, track version states
- **Infrastructure coordination** - Deploy multiple Kubernetes resources for each version  
- **Lifecycle monitoring** - Watch for drained versions and clean up resources
- **Rollout orchestration** - Manually control progressive traffic shifting

### The Controller Automates Everything

The Temporal Worker Controller eliminates this operational overhead by automating the entire Worker Versioning lifecycle on Kubernetes:

- **Automatic Temporal integration** - Registers versions and manages routing without manual API calls
- **Kubernetes-native workflow** - Update a single custom resource, get full [rainbow deployments](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning#deployment-systems)  
- **Intelligent cleanup** - Monitors version [drainage](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning#sunsetting-an-old-deployment-version) and automatically removes unused resources
- **Built-in rollout strategies** - Progressive, AllAtOnce, and Manual with configurable safety controls

## 📖 Documentation

| Document                                                    | Description                                                           |
|-------------------------------------------------------------|-----------------------------------------------------------------------|
| [Releases](docs/release.md)                                 | How we version and release the controller and Helm Chart              |
| [Migration Guide](docs/migration-to-versioned.md)           | Step-by-step guide for migrating from traditional deployments         |
| [Reversion Guide](docs/migration-to-unversioned.md)         | Step-by-step guide for migrating back to unversioned deployment       |
| [CD Rollouts](docs/cd-rollouts.md)                          | Helm, kubectl, ArgoCD, and Flux integration for steady-state rollouts |
| [Architecture](docs/architecture.md)                        | Technical deep-dive into how the controller works                     |
| [Configuration](docs/configuration.md)                      | Complete configuration reference                                      |
| [Concepts](docs/concepts.md)                                | Key concepts and terminology                                          |
| [Limits](docs/limits.md)                                    | Technical constraints and limitations                                 |
| [WorkerResourceTemplate](docs/worker-resource-templates.md) | Attach HPAs, PDBs, and other resources to each versioned Deployment   |
| [CRD Management](docs/crd-management.md)                    | CRD upgrade, rollback, and migration guide                            |

## 🔧 Worker Configuration

Your workers need these environment variables (automatically set by the controller):

```bash
TEMPORAL_ADDRESS=your-temporal-server:7233
TEMPORAL_NAMESPACE=your-namespace  
TEMPORAL_DEPLOYMENT_NAME=my-worker        # Unique worker deployment name
TEMPORAL_WORKER_BUILD_ID=v1.2.3          # Version identifier
```

**Important**: Don't set the above environment variables manually - the controller manages these automatically.

## 🤝 Contributing

We welcome all contributions! This includes:

- 🔧 **Code contributions** - Please start by [opening an issue](https://github.com/temporalio/temporal-worker-controller/issues/new) to discuss your idea
- 🐛 **Bug reports** - [File an issue](https://github.com/temporalio/temporal-worker-controller/issues/new)
- 💡 **Feature requests** - Tell us what you'd like to see
- 💬 **Feedback** - Join [#safe-deploys](https://temporalio.slack.com/archives/C07MDJ6S3HP) on [Temporal Slack](https://t.mp/slack)

## 🛠️ Development

Want to try the controller locally? Check out the [local demo guide](internal/demo/README.md) for development setup.

Need a test controller image from an unmerged branch? Run the `publish-branch-image` GitHub Actions workflow with the branch name.

## 📄 License

This project is licensed under the [MIT License](LICENSE).

---

**Questions?** Reach out to [@jlegrone](https://github.com/jlegrone) or the [#safe-deploys](https://temporalio.slack.com/archives/C07MDJ6S3HP) channel on Temporal Slack!
