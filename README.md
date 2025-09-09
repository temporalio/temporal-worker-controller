# Temporal Worker Controller

[![License](https://img.shields.io/github/license/temporalio/temporal-worker-controller)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/temporalio/temporal-worker-controller)](https://goreportcard.com/report/github.com/temporalio/temporal-worker-controller)

> 🚀 **Public Preview**: This project is in [Public Preview](https://docs.temporal.io/evaluate/development-production-features/release-stages) and ready for production use cases. Core functionality is complete with stable APIs.
> 
> ⚠️ Dynamic auto-scaling based on workflow load is not yet implemented. Use cases must work with fixed worker replica counts.

**The Temporal Worker Controller makes it simple and safe to deploy Temporal workers on Kubernetes.**

Instead of worrying about breaking running workflows when you deploy new code, the controller automatically manages multiple versions of your workers so that existing Pinned workflows continue running uninterrupted while new workflows and existing AutoUpgrade workflows use latest version.

## What does it do?

🔒 **Protected Pinned workflows** - Workflows pinned to a version stay on that version and won't break  
🎚️ **Controlled rollout for AutoUpgrade workflows** - AutoUpgrade workflows shifted to new versions with configurable safety controls  
📦 **Automatic version management** - Registers versions with Temporal, manages routing rules, and tracks version lifecycle  
🎯 **Smart traffic routing** - New workflows automatically get routed to your target worker version  
🛡️ **Progressive rollouts** - Catch incompatible changes early with small traffic percentages before they spread  
⚡ **Easy rollbacks** - Instantly route traffic back to a previous version if issues are detected  

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
4. 🧹 Automatically scales down and cleans up old versions once all Pinned workflows complete

## Getting Started

### Prerequisites

- Kubernetes cluster (1.19+) 
- [Temporal Server](https://docs.temporal.io/) (Cloud or self-hosted)
- Basic familiarity with Temporal [Workers](https://docs.temporal.io/workers) and [Workflows](https://docs.temporal.io/workflows)

### 🔧 Installation

```bash
# Install using Helm in your preferred namespace
helm install temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --namespace <your-namespace>
```

### Next Steps

**New to worker versioning?** → Start with our [Migration Guide](docs/migration-guide.md) to learn how to safely transition from traditional deployments.

**Ready to dive deeper?** → Check out the [Architecture Guide](docs/architecture.md) to understand how the controller works, or the [Temporal Worker Versioning docs](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) to learn about the underlying Temporal feature.

**Need configuration help?** → See the [Configuration Reference](docs/configuration.md) for all available options.

## Features

- ✅ **Registration of new Temporal Worker Deployment Versions**
- ✅ **Creation of versioned Deployment resources** (managing Pods that run your Temporal workers)
- ✅ **Automatic lifecycle scaling** - Scales down worker versions when no longer needed
- ✅ **Deletion of resources** associated with drained Worker Deployment Versions
- ✅ **Multiple rollout strategies**: `Manual`, `AllAtOnce`, and `Progressive` rollouts
- ✅ **Gate workflows** - Test new versions before routing real traffic to them
- ⏳ **Load-based auto-scaling** - Not yet implemented (use fixed replica counts)


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
- **Kubernetes-native workflow** - Update a single custom resource, get full rainbow deployments  
- **Intelligent cleanup** - Monitors version drainage and automatically removes unused resources
- **Built-in rollout strategies** - Progressive, AllAtOnce, and Manual with configurable safety controls

## 📖 Documentation

| Document | Description |
|----------|-------------|
| [Migration Guide](docs/migration-guide.md) | Step-by-step guide for migrating from traditional deployments |
| [Architecture](docs/architecture.md) | Technical deep-dive into how the controller works |
| [Configuration](docs/configuration.md) | Complete configuration reference |
| [Concepts](docs/concepts.md) | Key concepts and terminology |
| [Limits](docs/limits.md) | Technical constraints and limitations |

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

This project is in early stages. While external code contributions are not yet being solicited, we welcome:

- 🐛 **Bug reports** - [File an issue](https://github.com/temporalio/temporal-worker-controller/issues/new)
- 💡 **Feature requests** - Tell us what you'd like to see
- 💬 **Feedback** - Join [#safe-deploys](https://temporalio.slack.com/archives/C07MDJ6S3HP) on [Temporal Slack](https://t.mp/slack)

## 🛠️ Development

Want to try the controller locally? Check out the [local demo guide](internal/demo/README.md) for development setup.

## 📄 License

This project is licensed under the [MIT License](LICENSE).

---

**Questions?** Reach out to [@jlegrone](https://github.com/jlegrone) or the [#safe-deploys](https://temporalio.slack.com/archives/C07MDJ6S3HP) channel on Temporal Slack!