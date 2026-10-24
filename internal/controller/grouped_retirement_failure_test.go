// Grouped retirement tests exercise member planning and execution when a status
// read fails. Shared connection updates remain independent of replica decisions.
package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type (
	retirementReadFailureClient struct {
		*retirementClient
		failingID string
		failure   error
	}
)

func TestGroupedRetirementReadErrorsPreserveExistingMemberCounts(t *testing.T) {
	for _, failureAt := range []string{"approval", "history", "readiness"} {
		for _, memberState := range []string{"stopped", "missing", "different counts"} {
			t.Run(failureAt+"/"+memberState, func(t *testing.T) {
				r, _ := newTestReconciler(nil)
				old := groupedWorker("build-a")
				old.Spec.RolloutStrategy.Gate = &v1alpha1.GateWorkflowConfig{WorkflowType: "CheckReady"}
				saved, members := savedGroup(t, r, old)
				for _, member := range members {
					count := int32(0)
					if memberState == "different counts" {
						count = 1
					}
					member.Spec.Replicas = &count
				}
				if memberState == "missing" {
					delete(members, "activities")
				}
				next := groupedWorker("build-b")
				ts := registeredGroup("build-a")
				ts.CurrentBuildID = "build-b"
				p := groupPlan()
				p.WorkerDeploymentName = k8s.ComputeWorkerDeploymentName(next)
				require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, groupState("build-a", saved, members), p, false))
				base := approvedRetirements(t, p, nil)
				failure := errors.New("temporary retirement observation failure")
				var c sdkclient.Client = base
				switch failureAt {
				case "approval":
					c = &retirementReadFailureClient{retirementClient: base, failingID: retirementWorkflowID(p.WorkerDeploymentName, "build-a"), failure: failure}
				case "history":
					base.history.err = failure
				case "readiness":
					c = &retirementReadFailureClient{retirementClient: base, failingID: temporal.GetTestWorkflowID(p.WorkerDeploymentName, "build-a", "jobs"), failure: failure}
				}
				assert.ErrorIs(t, planGroupRetirements(context.Background(), c, p), failure)
				assert.Empty(t, p.CreateDeployments)
				assert.Empty(t, p.ScaleDeployments)
				assert.Empty(t, p.DeleteDeployments)
				assert.Empty(t, p.MarkRevisionsForDeletion)
			})
		}
	}
}

func TestGroupedPendingRetirementRestoresMissingMembersAndSavedCounts(t *testing.T) {
	p, h := retirementPlan(t, v1alpha1.VersionStatusDraining)
	group := &p.groupRetirements[0]
	zero := int32(0)
	group.actual["primary"].Spec.Replicas = &zero
	delete(group.actual, "activities")
	c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
	for _, info := range c.info {
		info.Status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	}
	require.NoError(t, planGroupRetirements(context.Background(), c, p))
	require.Len(t, p.CreateDeployments, 1)
	assert.Equal(t, "activities", p.CreateDeployments[0].Labels[k8s.MemberLabel])
	assert.EqualValues(t, 3, *p.CreateDeployments[0].Spec.Replicas)
	require.Len(t, p.ScaleDeployments, 1)
	for ref, count := range p.ScaleDeployments {
		assert.Equal(t, group.actual["primary"].Name, ref.Name)
		assert.EqualValues(t, 2, count)
	}
	assert.Empty(t, p.DeleteDeployments)
	assert.Empty(t, h.calls)
}

// A stopped obsolete group must stay stopped when reading its retirement
// approval fails. The desired group must still receive its missing members.
func TestStoppedGroupSurvivesRetirementReadFailure(t *testing.T) {
	for _, test := range []struct {
		name             string
		failRead         bool
		rotateConnection bool
	}{
		{name: "approval readable"},
		{name: "approval read unavailable", failRead: true},
		{name: "approval readable with connection update", rotateConnection: true},
		{name: "approval unavailable with connection update", failRead: true, rotateConnection: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			setup, _ := newTestReconciler(nil)
			old := groupedWorker("build-a")
			old.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{}
			old.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: 24 * time.Hour}
			saved, members := savedGroup(t, setup, old)
			objects := []client.Object{saved.Object}
			for _, member := range members {
				zero := int32(0)
				member.Spec.Replicas = &zero
				member.Generation = 2
				member.Status = appsv1.DeploymentStatus{ObservedGeneration: 2}
				objects = append(objects, member)
			}
			var scaleRequests []int32
			r, recorder := newTestReconcilerWithInterceptors(objects, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if subresource != "scale" {
						return c.SubResource(subresource).Update(ctx, obj, opts...)
					}
					options := (&client.SubResourceUpdateOptions{}).ApplyOptions(opts)
					scale, ok := options.SubResourceBody.(*autoscalingv1.Scale)
					require.True(t, ok)
					scaleRequests = append(scaleRequests, scale.Spec.Replicas)
					var member appsv1.Deployment
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &member); err != nil {
						return err
					}
					member.Spec.Replicas = &scale.Spec.Replicas
					return c.Update(ctx, &member)
				},
			})
			next := groupedWorker("build-b")
			ts := registeredGroup("build-a")
			ts.CurrentBuildID = "build-b"
			ts.Versions["build-a"].Status = v1alpha1.VersionStatusDrained
			whenDrained := time.Now().Add(-time.Hour)
			ts.Versions["build-a"].DrainedSince = &whenDrained
			p := groupPlan()
			p.WorkerDeploymentName = k8s.ComputeWorkerDeploymentName(next)
			p.TemporalNamespace = "test-namespace"
			state := groupState("build-a", saved, members)
			connection := v1alpha1.ConnectionSpec{HostPort: "test:7233"}
			if test.rotateConnection {
				connection.HostPort = "rotated:7233"
				shared, err := planner.GeneratePlan(logr.Discard(), state, &next.Status, &next.Spec, ts, connection, &planner.Config{RolloutStrategy: next.Spec.RolloutStrategy}, p.WorkerDeploymentName, r.MaxDeploymentVersionsIneligibleForDeletion, nil, false, nil, next.Name, next.UID)
				require.NoError(t, err)
				p.UpdateDeployments = shared.UpdateDeployments
				require.Len(t, p.UpdateDeployments, 2, "the shared planner updates both old connection templates without changing their actual zero counts")
				for _, update := range p.UpdateDeployments {
					require.Zero(t, *update.Spec.Replicas)
				}
			}
			require.NoError(t, r.planGroupedDeployments(next, connection, ts, state, p, true))
			assert.Empty(t, p.ScaleDeployments, "obsolete member decisions wait for retirement observations")
			require.Len(t, p.CreateDeployments, 2, "the desired group has two missing members")
			base := approvedRetirements(t, p, newStubTemporalClient(nil))
			var temporalClient sdkclient.Client = base
			if test.failRead {
				temporalClient = &retirementReadFailureClient{retirementClient: base, failingID: retirementWorkflowID(p.WorkerDeploymentName, "build-a"), failure: errors.New("temporary retirement status read failure")}
			}
			err := r.executePlan(ctx, logr.Discard(), next, temporalClient, p)
			assert.NoError(t, err, "an obsolete-group read failure must not stop desired-group work")
			for name, member := range members {
				var actual appsv1.Deployment
				require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(member), &actual))
				assert.Zero(t, *actual.Spec.Replicas, "obsolete member %s must stay stopped", name)
				if test.rotateConnection {
					assert.Equal(t, k8s.ComputeConnectionSpecHash(connection), actual.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation], "independently authorized connection changes must still apply")
				}
			}
			var actual appsv1.DeploymentList
			require.NoError(t, r.List(ctx, &actual, client.MatchingLabels{k8s.BuildIDLabel: "build-b"}))
			assert.Len(t, actual.Items, 2, "the desired group still progresses")
			assert.Empty(t, scaleRequests, "a failed observation must not restore old pods")
			if test.failRead {
				require.Positive(t, len(recorder.Events), "the status failure remains observable")
				assert.Contains(t, <-recorder.Events, ReasonGroupRetirementFailed)
			}
		})
	}
}

func (c *retirementReadFailureClient) DescribeWorkflowExecution(ctx context.Context, id, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if id == c.failingID {
		return nil, c.failure
	}
	return c.retirementClient.DescribeWorkflowExecution(ctx, id, runID)
}
