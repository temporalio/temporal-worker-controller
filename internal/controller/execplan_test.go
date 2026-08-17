// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/sdk/converter"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ─── WRT lifecycle test helpers ──────────────────────────────────────────────
//
// These tests drive the controller's own plan generation (generatePlan) and plan
// execution (executePlan) across multiple reconcile "cycles" against a fake client,
// asserting on the rendered WRT resources and the WRT status entries the cycles
// leave behind. They are regression tests for the version sunset / rollback
// lifecycle of WorkerResourceTemplate-rendered resources.

// makeExecplanTWD returns a TWD with a fixed UID suitable for driving generatePlan directly.
func makeExecplanTWD(name, namespace string) *temporaliov1alpha1.WorkerDeployment {
	twd := makeWD(name, namespace, "my-conn")
	twd.UID = types.UID("twd-uid-" + name)
	return twd
}

// makeVersionedDeployment builds the versioned Deployment the controller would create for
// the given build ID: deterministic name, build-id label, controller owner ref to the TWD,
// and the connection-spec hash annotation pre-set so plan generation does not schedule an
// update for it.
func makeVersionedDeployment(twd *temporaliov1alpha1.WorkerDeployment, buildID string, replicas int32, connection temporaliov1alpha1.ConnectionSpec) *appsv1.Deployment {
	isController := true
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8s.ComputeVersionedDeploymentName(twd.Name, buildID),
			Namespace: twd.Namespace,
			Labels:    map[string]string{k8s.BuildIDLabel: buildID},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: temporaliov1alpha1.GroupVersion.String(),
				Kind:       "WorkerDeployment",
				Name:       twd.Name,
				UID:        twd.UID,
				Controller: &isController,
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{k8s.BuildIDLabel: buildID}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{k8s.BuildIDLabel: buildID},
					Annotations: map[string]string{
						k8s.ConnectionSpecHashAnnotation: k8s.ComputeConnectionSpecHash(connection),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "worker", Image: "temporal/worker:v1"}},
				},
			},
		},
	}
}

// makeExecplanWRT builds a WRT (HPA template with scaleTargetRef auto-injection opted in)
// that references the TWD and already carries a controller owner reference to it, so that
// executePlan does not generate owner-ref patches during the test.
func makeExecplanWRT(name string, twd *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerResourceTemplate {
	hpaTemplate := map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec": map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{}, // opt in to auto-injection
			"minReplicas":    float64(1),
			"maxReplicas":    float64(5),
		},
	}
	raw, _ := json.Marshal(hpaTemplate)
	isController := true
	blockOwnerDeletion := true
	return &temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: twd.Namespace,
			UID:       types.UID("wrt-uid-" + name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         temporaliov1alpha1.GroupVersion.String(),
				Kind:               "WorkerDeployment",
				Name:               twd.Name,
				UID:                twd.UID,
				Controller:         &isController,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			WorkerDeploymentRef: &temporaliov1alpha1.WorkerDeploymentReference{Name: twd.Name},
			Template:            runtime.RawExtension{Raw: raw},
		},
	}
}

// renderWRT renders the WRT for the given deployment/build the same way the planner does,
// returning the rendered object and its hash.
func renderWRT(t *testing.T, wrt *temporaliov1alpha1.WorkerResourceTemplate, dep *appsv1.Deployment, buildID, temporalNamespace string) (*unstructured.Unstructured, string) {
	t.Helper()
	rendered, err := k8s.RenderWorkerResourceTemplate(wrt, dep, buildID, temporalNamespace)
	require.NoError(t, err)
	hash := k8s.ComputeRenderedObjectHash(rendered)
	require.NotEmpty(t, hash)
	return rendered, hash
}

// baseVersion builds a BaseWorkerDeploymentVersion pointing at the given Deployment.
func baseVersion(buildID string, dep *appsv1.Deployment, status temporaliov1alpha1.VersionStatus) temporaliov1alpha1.BaseWorkerDeploymentVersion {
	v := temporaliov1alpha1.BaseWorkerDeploymentVersion{
		BuildID: buildID,
		Status:  status,
	}
	if dep != nil {
		v.Deployment = k8s.NewObjectRef(dep)
	}
	return v
}

// runPlanCycle generates a plan from the given TWD status and executes it, simulating one
// reconcile cycle's k8s-side effects. Returns the generated plan so tests can assert on it.
func runPlanCycle(t *testing.T, r *WorkerDeploymentReconciler, twd *temporaliov1alpha1.WorkerDeployment, connection temporaliov1alpha1.ConnectionSpec, status temporaliov1alpha1.WorkerDeploymentStatus) *plan {
	t.Helper()
	ctx := context.Background()
	w := twd.DeepCopy()
	w.Status = status
	p, err := r.generatePlan(ctx, logr.Discard(), w, connection, &temporal.TemporalWorkerState{})
	require.NoError(t, err, "generatePlan failed")
	require.NoError(t, r.executePlan(ctx, logr.Discard(), w, newStubTemporalClient(nil), p), "executePlan failed")
	return p
}

// getRenderedHPA fetches a rendered HPA copy by name from the fake cluster.
func getRenderedHPA(r *WorkerDeploymentReconciler, namespace, name string) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("autoscaling/v2")
	obj.SetKind("HorizontalPodAutoscaler")
	return r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, obj)
}

// getWRTVersionStatuses fetches the WRT from the fake cluster and returns its status entries
// keyed by build ID.
func getWRTVersionStatuses(t *testing.T, r *WorkerDeploymentReconciler, namespace, name string) map[string]temporaliov1alpha1.WorkerResourceTemplateVersionStatus {
	t.Helper()
	wrt := &temporaliov1alpha1.WorkerResourceTemplate{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, wrt))
	byBuildID := make(map[string]temporaliov1alpha1.WorkerResourceTemplateVersionStatus, len(wrt.Status.Versions))
	for _, v := range wrt.Status.Versions {
		byBuildID[v.BuildID] = v
	}
	return byBuildID
}

// ─── Regression tests ────────────────────────────────────────────────────────

// TestExecutePlan_SunsetThenRedeploySameBuildID_ReappliesWorkerResource is a regression
// test for the rollback bug: sunsetting a version deletes its rendered WRT resource, but
// the WRT's per-build status entry (and its LastAppliedHash) survived, so redeploying the
// same build ID later (a rollback) rendered an identical object whose hash matched the
// stale entry — the SSA apply was skipped forever and the resource (e.g. a KEDA
// ScaledObject or HPA) was never recreated.
func TestExecutePlan_SunsetThenRedeploySameBuildID_ReappliesWorkerResource(t *testing.T) {
	const (
		namespace = "default"
		buildA    = "build-a"
		buildB    = "build-b"
	)
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makeExecplanTWD("my-worker", namespace)
	depA := makeVersionedDeployment(twd, buildA, 0, connection) // drained, scaled to zero
	depB := makeVersionedDeployment(twd, buildB, 1, connection) // current
	wrt := makeExecplanWRT("my-hpa", twd)

	// Render both versions' resources exactly like the planner will, and seed the WRT
	// status as if both were applied in earlier cycles.
	hpaA, hashA := renderWRT(t, wrt, depA, buildA, testTemporalNamespace)
	hpaB, hashB := renderWRT(t, wrt, depB, buildB, testTemporalNamespace)
	wrt.Status.Versions = []temporaliov1alpha1.WorkerResourceTemplateVersionStatus{
		k8s.WorkerResourceTemplateVersionStatusForBuildID(buildA, hpaA.GetName(), 1, hashA, ""),
		k8s.WorkerResourceTemplateVersionStatusForBuildID(buildB, hpaB.GetName(), 1, hashB, ""),
	}

	r, _ := newTestReconciler([]client.Object{twd, depA, depB, wrt, hpaA, hpaB})

	// ── Cycle 1: sunset build-a (drained past delays, scaled to zero) ──
	drainedSince := metav1.NewTime(time.Now().Add(-time.Hour))
	sunsetStatus := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion:  temporaliov1alpha1.TargetWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			{
				BaseWorkerDeploymentVersion: baseVersion(buildA, depA, temporaliov1alpha1.VersionStatusDrained),
				DrainedSince:                &drainedSince,
			},
		},
	}
	p1 := runPlanCycle(t, r, twd, connection, sunsetStatus)
	require.Len(t, p1.DeleteDeployments, 1, "cycle 1 must delete the drained deployment")
	require.NotEmpty(t, p1.DeleteWorkerResources, "cycle 1 must delete the sunset version's rendered resource")

	// The deployment and its rendered resource are gone; the current version's copy remains.
	require.True(t, apierrors.IsNotFound(r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: depA.Name}, &appsv1.Deployment{})), "deployment for build-a should be deleted")
	require.True(t, apierrors.IsNotFound(getRenderedHPA(r, namespace, hpaA.GetName())), "rendered resource for build-a should be deleted at sunset")
	require.NoError(t, getRenderedHPA(r, namespace, hpaB.GetName()), "rendered resource for build-b must survive the sunset of build-a")

	// The WRT status entry for the sunset build must be pruned together with the resource;
	// a stale entry would poison a future redeploy of the same build ID with a hash skip.
	statuses := getWRTVersionStatuses(t, r, namespace, wrt.Name)
	require.NotContains(t, statuses, buildA, "WRT status entry for the sunset build must be pruned when its rendered resource is deleted")
	require.Contains(t, statuses, buildB, "WRT status entry for the live build must be retained")

	// ── Cycle 2: roll back to build-a (same build ID redeployed) ──
	depA2 := makeVersionedDeployment(twd, buildA, 1, connection)
	require.NoError(t, r.Create(context.Background(), depA2))
	rollbackStatus := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion:  temporaliov1alpha1.TargetWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildA, depA2, temporaliov1alpha1.VersionStatusInactive)},
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
	}
	p2 := runPlanCycle(t, r, twd, connection, rollbackStatus)
	require.Empty(t, p2.DeleteWorkerResources, "redeployed build must not have its rendered resource scheduled for deletion")

	// The rendered resource for build-a must exist again: this is the core regression.
	require.NoError(t, getRenderedHPA(r, namespace, hpaA.GetName()), "rendered resource for the redeployed build ID must be re-applied after a sunset")

	statuses = getWRTVersionStatuses(t, r, namespace, wrt.Name)
	entry, ok := statuses[buildA]
	require.True(t, ok, "WRT status must have an entry for the redeployed build")
	require.Equal(t, hashA, entry.LastAppliedHash, "re-applied entry must record the rendered hash")
}

// TestExecutePlan_WRTResourceDeleteFailure_RetriedNextCycle is a regression test for the
// mirror bug: when the rendered resource delete at sunset failed after the Deployment
// delete succeeded, nothing retried it — delete candidates were derived only from
// Deployments that still exist, so the rendered resource (e.g. a KEDA ScaledObject
// polling the Temporal API) was orphaned until the WRT itself was deleted.
func TestExecutePlan_WRTResourceDeleteFailure_RetriedNextCycle(t *testing.T) {
	const (
		namespace = "default"
		buildA    = "build-a"
		buildB    = "build-b"
	)
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makeExecplanTWD("my-worker", namespace)
	depA := makeVersionedDeployment(twd, buildA, 0, connection)
	depB := makeVersionedDeployment(twd, buildB, 1, connection)
	wrt := makeExecplanWRT("my-hpa", twd)

	hpaA, hashA := renderWRT(t, wrt, depA, buildA, testTemporalNamespace)
	hpaB, hashB := renderWRT(t, wrt, depB, buildB, testTemporalNamespace)
	wrt.Status.Versions = []temporaliov1alpha1.WorkerResourceTemplateVersionStatus{
		k8s.WorkerResourceTemplateVersionStatusForBuildID(buildA, hpaA.GetName(), 1, hashA, ""),
		k8s.WorkerResourceTemplateVersionStatusForBuildID(buildB, hpaB.GetName(), 1, hashB, ""),
	}

	// Fail deletes of rendered HPA copies while failHPADeletes is true; Deployment
	// deletes always succeed, reproducing "Deployment gone, rendered resource orphaned".
	failHPADeletes := true
	r, _ := newTestReconcilerWithInterceptors([]client.Object{twd, depA, depB, wrt, hpaA, hpaB}, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if failHPADeletes && obj.GetObjectKind().GroupVersionKind().Kind == "HorizontalPodAutoscaler" {
				return errors.New("simulated transient delete failure")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})

	// ── Cycle 1: sunset build-a; the rendered resource delete fails ──
	drainedSince := metav1.NewTime(time.Now().Add(-time.Hour))
	sunsetStatus := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion:  temporaliov1alpha1.TargetWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			{
				BaseWorkerDeploymentVersion: baseVersion(buildA, depA, temporaliov1alpha1.VersionStatusDrained),
				DrainedSince:                &drainedSince,
			},
		},
	}
	p1 := runPlanCycle(t, r, twd, connection, sunsetStatus)
	require.NotEmpty(t, p1.DeleteWorkerResources, "cycle 1 must attempt to delete the sunset version's rendered resource")

	// Deployment is gone, but the rendered resource survived the failed delete.
	require.True(t, apierrors.IsNotFound(r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: depA.Name}, &appsv1.Deployment{})), "deployment for build-a should be deleted")
	require.NoError(t, getRenderedHPA(r, namespace, hpaA.GetName()), "rendered resource for build-a should still exist after the failed delete")

	// The status entry must NOT be pruned on a failed delete — it is what drives the retry.
	statuses := getWRTVersionStatuses(t, r, namespace, wrt.Name)
	require.Contains(t, statuses, buildA, "WRT status entry must be retained while its rendered resource still exists")

	// ── Cycle 2: no Deployment for build-a exists anymore; the delete must be retried ──
	failHPADeletes = false
	steadyStatus := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion:  temporaliov1alpha1.TargetWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
	}
	p2 := runPlanCycle(t, r, twd, connection, steadyStatus)
	require.NotEmpty(t, p2.DeleteWorkerResources, "delete of the orphaned rendered resource must be retried even though its Deployment no longer exists")

	require.True(t, apierrors.IsNotFound(getRenderedHPA(r, namespace, hpaA.GetName())), "orphaned rendered resource must be deleted on retry")

	statuses = getWRTVersionStatuses(t, r, namespace, wrt.Name)
	require.NotContains(t, statuses, buildA, "WRT status entry must be pruned once the rendered resource is deleted")
	require.Contains(t, statuses, buildB, "WRT status entry for the live build must be retained")
}

// TestExecutePlan_AllAppliesSkippedWithoutDeletes_SkipsStatusWrite guards the skip
// optimisation: when every apply for a WRT is a hash-skip and nothing was deleted,
// executePlan must not touch the WRT at all (no status write, no resourceVersion bump).
func TestExecutePlan_AllAppliesSkippedWithoutDeletes_SkipsStatusWrite(t *testing.T) {
	const (
		namespace = "default"
		buildB    = "build-b"
	)
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makeExecplanTWD("my-worker", namespace)
	depB := makeVersionedDeployment(twd, buildB, 1, connection)
	wrt := makeExecplanWRT("my-hpa", twd)

	hpaB, hashB := renderWRT(t, wrt, depB, buildB, testTemporalNamespace)
	wrt.Status.Versions = []temporaliov1alpha1.WorkerResourceTemplateVersionStatus{
		k8s.WorkerResourceTemplateVersionStatusForBuildID(buildB, hpaB.GetName(), 1, hashB, ""),
	}

	r, _ := newTestReconciler([]client.Object{twd, depB, wrt, hpaB})

	before := &temporaliov1alpha1.WorkerResourceTemplate{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: wrt.Name}, before))

	steadyStatus := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion:  temporaliov1alpha1.TargetWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: baseVersion(buildB, depB, temporaliov1alpha1.VersionStatusCurrent)},
	}
	p := runPlanCycle(t, r, twd, connection, steadyStatus)
	require.Len(t, p.ApplyWorkerResources, 1)
	require.Empty(t, p.DeleteWorkerResources)

	after := &temporaliov1alpha1.WorkerResourceTemplate{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: wrt.Name}, after))
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "no-op cycle must not write WRT status")
}

// ─── gateWorkflowArg tests ───────────────────────────────────────────────────
//
// gateWorkflowArg decides how the gate input is handed to the Temporal SDK. The
// assertions below run the result through the SDK's own default data converter —
// the same path ExecuteWorkflow uses — so they verify the payload the worker
// actually receives rather than just the shape of the intermediate value.

// TestGateWorkflowArg_NoEncoding_ProducesJSONPlain pins the behavior that existed before
// the encoding field: a gate with no declared encoding must still produce a json/plain
// payload carrying the input verbatim. The Go type is what drives that choice, so
// returning the same bytes wrapped differently would silently re-encode every existing
// gate workflow.
func TestGateWorkflowArg_NoEncoding_ProducesJSONPlain(t *testing.T) {
	input := []byte(`{"service":"checkout"}`)

	arg := gateWorkflowArg(startWorkflowConfig{input: input})

	raw, ok := arg.(json.RawMessage)
	require.True(t, ok, "expected json.RawMessage, got %T", arg)
	require.Equal(t, input, []byte(raw))

	payloads, err := converter.GetDefaultDataConverter().ToPayloads(arg)
	require.NoError(t, err)
	require.Len(t, payloads.GetPayloads(), 1)

	p := payloads.GetPayloads()[0]
	require.Equal(t, converter.MetadataEncodingJSON, string(p.Metadata[converter.MetadataEncoding]))
	require.Equal(t, input, p.Data)
	require.NotContains(t, p.Metadata, converter.MetadataMessageType)
}

// A message type with no encoding cannot reach the controller through the CRD (both the
// CEL rules and the webhook reject it), but the helper must not treat it as a reason to
// hand-build a payload: without an encoding there is nothing to declare.
func TestGateWorkflowArg_MessageTypeWithoutEncoding_ProducesJSONPlain(t *testing.T) {
	arg := gateWorkflowArg(startWorkflowConfig{
		input:       []byte(`{"service":"checkout"}`),
		messageType: "my.package.DeployRequest",
	})

	_, ok := arg.(json.RawMessage)
	require.True(t, ok, "expected json.RawMessage, got %T", arg)
}

// Every encoding in the enum must reach the payload untouched, and the input bytes must
// be passed through without transformation — the controller labels the data, it never
// re-encodes it.
func TestGateWorkflowArg_EachEncoding_IsDeclaredVerbatim(t *testing.T) {
	for _, encoding := range []temporaliov1alpha1.PayloadMetadataEncodingType{
		temporaliov1alpha1.PayloadMetadataEncodingTypeBinary,
		temporaliov1alpha1.PayloadMetadataEncodingTypeJSON,
		temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON,
		temporaliov1alpha1.PayloadMetadataEncodingTypeProto,
	} {
		t.Run(string(encoding), func(t *testing.T) {
			input := []byte{0x0a, 0x08, 0x63, 0x68, 0x65, 0x63, 0x6b}

			payloads, err := converter.GetDefaultDataConverter().ToPayloads(
				gateWorkflowArg(startWorkflowConfig{input: input, encoding: string(encoding)}),
			)
			require.NoError(t, err)
			require.Len(t, payloads.GetPayloads(), 1)

			p := payloads.GetPayloads()[0]
			require.Equal(t, string(encoding), string(p.Metadata[converter.MetadataEncoding]))
			require.Equal(t, input, p.Data)
			require.NotContains(t, p.Metadata, converter.MetadataMessageType,
				"messageType key must be absent, not empty, when no message type is set")
		})
	}
}

// When a message type is supplied it is recorded alongside the encoding. Both keys must
// survive the SDK's converter untouched, which is the whole point of using RawValue.
func TestGateWorkflowArg_MessageTypeSet_RecordedAlongsideEncoding(t *testing.T) {
	input := []byte(`{"service":"checkout","replicas":3}`)

	payloads, err := converter.GetDefaultDataConverter().ToPayloads(
		gateWorkflowArg(startWorkflowConfig{
			input:       input,
			encoding:    string(temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON),
			messageType: "my.package.DeployRequest",
		}),
	)
	require.NoError(t, err)
	require.Len(t, payloads.GetPayloads(), 1)

	p := payloads.GetPayloads()[0]
	require.Equal(t, converter.MetadataEncodingProtoJSON, string(p.Metadata[converter.MetadataEncoding]))
	require.Equal(t, "my.package.DeployRequest", string(p.Metadata[converter.MetadataMessageType]))
	require.Equal(t, input, p.Data)
}

// TestGeneratePlan_CarriesEncodingAndMessageType covers the second half of the transport:
// the planner's WorkflowConfig is copied field by field into the controller's own
// startWorkflowConfig, and a missed assignment there would drop the encoding just as
// silently as one in the planner.
func TestGeneratePlan_CarriesEncodingAndMessageType(t *testing.T) {
	namespace := "default"
	twd := makeExecplanTWD("gate-encoding-worker", namespace)
	twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{
		WorkflowType: "VerifyDeploy",
		Input:        &apiextensionsv1.JSON{Raw: []byte(`{"service":"checkout"}`)},
		Encoding:     temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON,
		MessageType:  "my.package.DeployRequest",
	}

	r, _ := newTestReconcilerWithInterceptors([]client.Object{twd}, interceptor.Funcs{})

	w := twd.DeepCopy()
	w.Status = temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
				BuildID: "build-abc",
				Status:  temporaliov1alpha1.VersionStatusInactive,
				TaskQueues: []temporaliov1alpha1.TaskQueue{
					{Name: "queue1"},
				},
			},
		},
	}

	p, err := r.generatePlan(context.Background(), logr.Discard(), w,
		temporaliov1alpha1.ConnectionSpec{}, &temporal.TemporalWorkerState{})
	require.NoError(t, err)
	require.Len(t, p.startTestWorkflows, 1)

	wf := p.startTestWorkflows[0]
	require.Equal(t, string(temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON), wf.encoding)
	require.Equal(t, "my.package.DeployRequest", wf.messageType)
	require.Equal(t, []byte(`{"service":"checkout"}`), wf.input)
}
