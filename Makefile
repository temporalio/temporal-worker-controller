# Image URL to use all building/pushing image targets
IMG ?= temporal-worker-controller:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.27.1

MAIN_BRANCH = main
ALL_TEST_TAGS = test_dep

##### Variables ######

ROOT := $(shell git rev-parse --show-toplevel)
## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)
STAMPDIR := .stamp
export PATH := $(ROOT)/$(LOCALBIN):$(PATH)
GOINSTALL := GOBIN=$(ROOT)/$(LOCALBIN) go install

OTEL ?= false
ifeq ($(OTEL),true)
	export OTEL_BSP_SCHEDULE_DELAY=100 # in ms
	export OTEL_EXPORTER_OTLP_TRACES_INSECURE=true
	export OTEL_TRACES_EXPORTER=otlp
	export TEMPORAL_OTEL_DEBUG=true
endif

MODULE_ROOT := $(lastword $(shell grep -e "^module " go.mod))

## Tool Binaries
KUBECTL ?= kubectl
K8S_CONTEXT ?= minikube
HELM ?= $(LOCALBIN)/helm
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
TEMPORAL ?= temporal

## Tool Versions
HELM_VERSION ?= v3.14.3
CONTROLLER_TOOLS_VERSION ?= v0.19.0

##### Tools #####
print-go-version:
	@go version

clean-tools:
	@printf $(COLOR) "Delete tools..."
	@rm -rf $(STAMPDIR)
	@rm -rf $(LOCALBIN)

$(STAMPDIR):
	@mkdir -p $(STAMPDIR)

# When updating the version, update the golangci-lint GHA workflow as well.
.PHONY: golangci-lint
GOLANGCI_LINT_BASE_REV ?= $(MAIN_BRANCH)
GOLANGCI_LINT_FIX ?= true
GOLANGCI_LINT_VERSION := v1.64.8
GOLANGCI_LINT := $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# Don't get confused, there is a single linter called gci, which is a part of the mega linter we use is called golangci-lint.
GCI_VERSION := v0.13.6
GCI := $(LOCALBIN)/gci-$(GCI_VERSION)
$(GCI): $(LOCALBIN)
	$(call go-install-tool,$(GCI),github.com/daixiang0/gci,$(GCI_VERSION))

GOTESTSUM_VER := v1.12.1
GOTESTSUM := $(LOCALBIN)/gotestsum-$(GOTESTSUM_VER)
$(GOTESTSUM): | $(LOCALBIN)
	$(call go-install-tool,$(GOTESTSUM),gotest.tools/gotestsum,$(GOTESTSUM_VER))

GO_API_VER = $(shell go list -m -f '{{.Version}}' go.temporal.io/api \
	|| (echo "failed to fetch version for go.temporal.io/api" >&2))

ACTIONLINT_VER := v1.7.7
ACTIONLINT := $(LOCALBIN)/actionlint-$(ACTIONLINT_VER)
$(ACTIONLINT): | $(LOCALBIN)
	$(call go-install-tool,$(ACTIONLINT),github.com/rhysd/actionlint/cmd/actionlint,$(ACTIONLINT_VER))


# The following tools need to have a consistent name, so we use a versioned stamp file to ensure the version we want is installed
# while installing to an unversioned binary name.
GOIMPORTS_VER := v0.31.0
GOIMPORTS := $(LOCALBIN)/goimports
$(STAMPDIR)/goimports-$(GOIMPORTS_VER): | $(STAMPDIR) $(LOCALBIN)
	$(call go-install-tool,$(GOIMPORTS),golang.org/x/tools/cmd/goimports,$(GOIMPORTS_VER))
	@touch $@
$(GOIMPORTS): $(STAMPDIR)/goimports-$(GOIMPORTS_VER)

COLOR := "\e[1;36m%s\e[0m\n"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
# This is courtesy of https://github.com/kubernetes-sigs/kubebuilder/pull/3718
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
printf $(COLOR) "Downloading $${package}" ;\
tmpdir=$$(mktemp -d) ;\
GOBIN=$${tmpdir} go install $${package} ;\
mv $${tmpdir}/$$(basename "$$(echo "$(1)" | sed "s/-$(3)$$//")") $(1) ;\
rm -rf $${tmpdir} ;\
}
endef

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
	GOWORK=off GO111MODULE=on $(CONTROLLER_GEN) rbac:roleName=manager-role crd:allowDangerousTypes=true,maxDescLen=0,generateEmbeddedObjectMeta=true webhook paths=./api/... paths=./internal/... paths=./cmd/... \
    output:crd:artifacts:config=helm/temporal-worker-controller/templates/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	GOWORK=off GO111MODULE=on $(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths=./api/... paths=./internal/... paths=./cmd/...

.PHONY: start-sample-workflow
.SILENT: start-sample-workflow
start-sample-workflow: ## Start a sample workflow.
	@set -e; \
	# Load env vars from skaffold.env if present so address/namespace aren't hardcoded
	if [ -f skaffold.env ]; then set -a; . skaffold.env; set +a; fi; \
	API_KEY_VAL=""; \
	if [ -n "$$TEMPORAL_API_KEY" ]; then API_KEY_VAL="$$TEMPORAL_API_KEY"; \
	elif [ -f certs/api-key.txt ]; then API_KEY_VAL="$$(tr -d '\r\n' < certs/api-key.txt)"; fi; \
	if [ -n "$$API_KEY_VAL" ]; then \
	  $(TEMPORAL) workflow start --type "HelloWorld" --task-queue "default/helloworld" \
	    --address "$$TEMPORAL_ADDRESS" \
	    --namespace "$$TEMPORAL_NAMESPACE" \
	    --api-key "$$API_KEY_VAL"; \
	else \
	  $(TEMPORAL) workflow start --type "HelloWorld" --task-queue "default/helloworld" \
	    --tls-cert-path certs/client.pem \
	    --tls-key-path certs/client.key \
	    --address "$$TEMPORAL_ADDRESS" \
	    --namespace "$$TEMPORAL_NAMESPACE"; \
	fi

.PHONY: apply-load-sample-workflow
.SILENT: apply-load-sample-workflow
apply-load-sample-workflow: ## Start a sample workflow every 15 seconds
	@while true; do \
		$(MAKE) -s start-sample-workflow; \
		sleep 15; \
	done

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

.PHONY: test-all
test-all: manifests generate envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags test_dep ./... -coverprofile cover.out

.PHONY: test-unit
test-unit: ## Run unit tests with minimal setup.
	go test ./... -coverprofile cover.out

.PHONY: test-integration
test-integration: manifests generate envtest ## Run integration tests against local Temporal dev server.
	@echo "Running integration tests..."
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -v -tags test_dep ./internal/tests/internal -run TestIntegration

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
docker-build: ## Build docker image with the manager.
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
# ignore-not-found is used in the uninstall target
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
	helm install temporal-worker-controller ./helm/temporal-worker-controller --create-namespace --namespace temporal-system

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	helm uninstall temporal-worker-controller --namespace temporal-system

##@ Build Dependencies

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

# Create an mTLS secret in k8s
.PHONY: create-cloud-mtls-secret
create-cloud-mtls-secret:
	kubectl create secret tls temporal-cloud-mtls --namespace default \
      --cert=certs/client.pem \
      --key=certs/client.key

.PHONY: create-api-key-secret
create-api-key-secret:
	kubectl create secret generic temporal-api-key --namespace default \
      --from-file=api-key=certs/api-key.txt

##### Checks #####
goimports: fmt-imports $(GOIMPORTS)
	@printf $(COLOR) "Run goimports for all files..."
	@UNGENERATED_FILES=$$(find . -type f -name '*.go' -print0 | xargs -0 grep -L -e "Code generated by .* DO NOT EDIT." || true) && \
		$(GOIMPORTS) -w $$UNGENERATED_FILES


lint-actions: $(ACTIONLINT)
	@printf $(COLOR) "Linting GitHub actions..."
	@$(ACTIONLINT)

lint-code: $(GOLANGCI_LINT)
	@printf $(COLOR) "Linting code..."
	@$(GOLANGCI_LINT) run --verbose --build-tags $(ALL_TEST_TAGS) --timeout 10m --fix=$(GOLANGCI_LINT_FIX) --new-from-rev=$(GOLANGCI_LINT_BASE_REV) --config=.github/.golangci.yml

fmt-imports: $(GCI) # Don't get confused, there is a single linter called gci, which is a part of the mega linter we use is called golangci-lint.
	@printf $(COLOR) "Formatting imports..."
	@$(GCI) write --skip-generated -s standard -s default ./api ./cmd ./internal

lint: lint-code lint-actions
	@printf $(COLOR) "Run linters..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...
