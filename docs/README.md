# Temporal Worker Controller Documentation

This directory contains extended documentation for the Temporal Worker Controller project. The documentation is organized into different categories to help you find the information you need.


This documentation structure is designed to support various types of technical documentation, such as:

- **Limits**: Technical constraints and limitations of the system
- **Runbooks**: Operational procedures and troubleshooting guides (planned)
- **Conceptual Guides**: High-level explanations of concepts and architecture (planned)

## Index

### [CD Rollouts](cd-rollouts.md)
How to integrate the controller into Helm, kubectl, ArgoCD, and Flux pipelines for steady-state rollouts once you are already using Worker Versioning.

### [Architecture](architecture.md)
High-level overview of the Temporal Worker Controller architecture.

### [Concepts](concepts.md)
Conceptual guides for the Temporal Worker Controller system.

### [Configuration](configuration.md)
Configuration options for the Temporal Worker Controller.

### [CRD Management](crd-management.md)
How to install and upgrade the Temporal Worker Controller Custom Resource Definitions (CRDs).

### [Limits](limits.md)
Technical constraints and limitations of the Temporal Worker Controller system, including maximum field lengths and other operational boundaries.

### [Migration Guide](migration-to-versioned.md)
Comprehensive guide for migrating from existing unversioned worker deployment systems to the Temporal Worker Controller. Includes step-by-step instructions, configuration mapping, and common patterns.
See [Migration to Unversioned](migration-to-unversioned.md) for how to migrate back to an unversioned deployment system.

### [Ownership](ownership.md)
How the controller gets permission to manage a Worker Deployment, how a human client can take or give back control.

### [WorkerResourceTemplate](worker-resource-templates.md)
How to attach HPAs, PodDisruptionBudgets, and other Kubernetes resources to each active versioned Deployment. Covers the auto-injection model, RBAC setup, webhook TLS, and examples.

---

*Note: This documentation structure is designed to grow with the project.*