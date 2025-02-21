
 ## Demo
 
This demo assumes you have [skaffold](https://skaffold.dev/docs/install/) and [minikube](https://minikube.sigs.k8s.io/docs/) and installed, but any
Kubernetes cluster should work if you're able to push the demo worker image.

1. Start local Temporal server:
    ```bash
    make start-temporal-server
    ```
1. Open Temporal UI: http://localhost:8233/namespaces/default/workflows
1. Start local k8s:
    ```bash
    minikube start
    ```
1. Start the controller:
    ```bash
    skaffold dev --profile manager
    ```
1. Start applying load:
    ```bash
    make apply-load-sample-workflow
    ```
1. Inspect versioning rules: http://localhost:8233/namespaces/default/task-queues/hello_world
1. Deploy worker v1:
    ```bash
    skaffold run --profile helloworld
    ```
1. Make a non-replay-safe change and redeploy:
    ```bash
    git apply internal/demo/helloworld/changes/no-version-gate.patch
    skaffold run --profile helloworld
    ```
   1. Observe multiple worker versions making progress in parallel
   1. Observe that v1 eventually scales down after ~1 minute
   1. Note that it's not fully deleted yet in order to support rolling back the default version externally,
      eg. with the `temporal` CLI
1. Update to a progressive rollout strategy:
    ```bash
    git apply internal/demo/helloworld/changes/progressive-rollout.patch
    ```
1. Make another change to the worker and redeploy:
    ```bash
    skaffold run --profile helloworld
    ```
1. Observe that new workflows are being started on both the new and previous worker versions until the rollout completes
