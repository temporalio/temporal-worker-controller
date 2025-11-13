module github.com/temporalio/temporal-worker-controller/tests/compat-v1.29.1

go 1.24.5

require (
	github.com/temporalio/temporal-worker-controller v1.0.1
	github.com/temporalio/temporal-worker-controller/tests v0.0.0
	go.temporal.io/api v1.50.1
	go.temporal.io/sdk v1.35.0
	go.temporal.io/server v1.29.1
	k8s.io/api v0.34.0
	k8s.io/apimachinery v0.34.0
	k8s.io/client-go v0.34.0
	sigs.k8s.io/controller-runtime v0.21.0
)

replace github.com/temporalio/temporal-worker-controller => ../../../..

replace github.com/temporalio/temporal-worker-controller/tests => ../..
