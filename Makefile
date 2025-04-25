# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.27.1

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

# crd:maxDescLen=0 is to avoid error described in https://github.com/kubernetes-sigs/kubebuilder/issues/2556#issuecomment-1074844483
.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:allowDangerousTypes=true,maxDescLen=0,generateEmbeddedObjectMeta=true webhook paths="./..." \
    output:crd:artifacts:config=helm/temporal-worker-controller/templates/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# source secret.env && make start-sample-workflow TEMPORAL_CLOUD_API_KEY=$TEMPORAL_CLOUD_API_KEY
.PHONY: start-sample-workflow
start-sample-workflow: ## Start a sample workflow.
	@$(TEMPORAL) workflow start --type "HelloWorld" --task-queue "hello_world" \
      --tls-cert-path certs/ca.pem \
      --tls-key-path certs/ca.key \
      --address "worker-controller-test.a2dd6.tmprl.cloud:7233" \
      -n "worker-controller-test.a2dd6"
#      --address replay-2025.ktasd.tmprl.cloud:7233 \
#      --api-key $(TEMPORAL_CLOUD_API_KEY)

.PHONY: apply-load-sample-workflow
apply-load-sample-workflow: ## Start a sample workflow every 15 seconds
	watch --interval 0.1 -- $(TEMPORAL) workflow start --type "HelloWorld" --task-queue "hello_world"

.PHONY: list-workflow-build-ids
list-workflow-build-ids: ## List workflow executions and their build IDs.
	@$(TEMPORAL) workflow list --limit 20 --fields SearchAttributes -o json | \
		jq '.[] | {buildID: .search_attributes.indexed_fields.BuildIds.data, workflowID: .execution.workflow_id, closeTime: .close_time, status}' | \
		jq '.buildID |= @base64d' | \
		jq -r '[.workflowID, .buildID, .status, .closeTime] | @tsv'

.PHONY: build-sample-worker
build-sample-worker: ## Build the sample worker container image.
	minikube image build -t worker-controller/sample-worker:latest -f sample-worker.dockerfile . && minikube image load --overwrite=true --daemon=true worker-controller/sample-worker:latest

.PHONY: deploy-sample-worker
deploy-sample-worker: build-sample-worker ## Deploy the sample worker to the cluster.
	$(KUBECTL) apply -f internal/demo/temporal_worker.yaml

.PHONY: start-temporal-server
start-temporal-server: ## Start an ephemeral Temporal server with versioning APIs enabled.
	$(TEMPORAL) server start-dev --ip 0.0.0.0 \
		--dynamic-config-value frontend.workerVersioningWorkflowAPIs=true \
		--dynamic-config-value system.enableDeploymentVersions=true

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish built the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64 ). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: test ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for  the manager image be build to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - able to use docker buildx . More info: https://docs.docker.com/build/buildx/
# - have enable BuildKit, More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To properly provided solutions that supports more than one platform you should use this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: test ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.cross

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUBECTL) apply --context $(K8S_CONTEXT) -f helm/temporal-worker-controller/templates/crds

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUBECTL) delete --context $(K8S_CONTEXT) --ignore-not-found=$(ignore-not-found) -f helm/temporal-worker-controller/templates/crds

.PHONY: deploy
deploy: manifests helm ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	helm install temporal-worker-controller ./helm/temporal-worker-controller --namespace temporal-system

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	helm uninstall temporal-worker-controller --namespace temporal-system

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
K8S_CONTEXT ?= minikube
HELM ?= $(LOCALBIN)/helm
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
TEMPORAL ?= temporal

## Tool Versions
HELM_VERSION ?= v3.14.3
CONTROLLER_TOOLS_VERSION ?= v0.16.2

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary. If wrong version is installed, it will be removed before downloading.
$(HELM): $(LOCALBIN)
	@if test -x $(LOCALBIN)/helm && ! $(LOCALBIN)/helm version | grep -q $(HELM_VERSION); then \
		echo "$(LOCALBIN)/helm version is not expected $(HELM_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/helm; \
	fi
	test -s $(LOCALBIN)/helm || curl -fsSL -o get_helm.sh \
                                https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
                                && chmod 700 get_helm.sh \
                                && GOBIN=$(LOCALBIN) GO111MODULE=on DESIRED_VERSION=$(HELM_VERSION) ./get_helm.sh \
                                && rm get_helm.sh

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

DD_SITE ?= datadoghq.com

# Add an entry in secret.env and then run this target via the following command:
#  source secret.env && make install-datadog-agent DD_API_KEY=$DD_API_KEY
#
# A DD_API_KEY can be found in the Datadog setup commands or on the API Keys page https://us1.datadoghq.com/organization-settings/api-keys
.PHONY: install-datadog-agent
install-datadog-agent:
	helm repo add datadog https://helm.datadoghq.com
	helm repo update
	kubectl delete secret datadog-api-key --namespace datadog-agent || true
	@helm upgrade --install --create-namespace datadog-agent datadog/datadog \
		--kube-context minikube \
		--namespace datadog-agent \
		-f hack/datadog-values.yaml \
		--set datadog.site='$(DD_SITE)'
	@kubectl create secret generic datadog-api-key --from-literal api-key=$(DD_API_KEY) --namespace datadog-agent

# Create an mTLS secret in k8s
.PHONY: create-cloud-mtls-secret
create-cloud-mtls-secret:
	kubectl create secret tls temporal-cloud-mtls --namespace default \
      --cert=certs/ca.pem \
      --key=certs/ca.key

# View workflows filtered by Build ID
# http://0.0.0.0:8233/namespaces/default/workflows?query=BuildIds+IN+%28%22versioned%3A5578f87d9c%22%29
