package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// runRateLimitTest verifies that when multiple TWDs in the same Temporal namespace
// burst-reconcile concurrently against a 1 RPS DescribeWorkerDeployment limit, the
// controller surfaces the error as ConditionProgressing=False with
// ReasonTemporalStateFetchFailed and a "Rate limited" message.
//
// The server passed in must have frontend.globalNamespaceWorkerDeploymentReadRPS=1.
// With 10 TWDs each reconciling every 1s (RECONCILE_INTERVAL=1s set by setupTestEnvironment),
// steady-state load is 10x the limit, ensuring the burst is reliably triggered.
func runRateLimitTest(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	testNamespace string,
) {
	t.Run("rate-limit-burst-surfaces-error", func(t *testing.T) {
		ctx := context.Background()
		const n = 10

		twds := make([]*temporaliov1alpha1.TemporalWorkerDeployment, n)

		for i := 0; i < n; i++ {
			name := fmt.Sprintf("rate-limit-%d", i)

			conn := &temporaliov1alpha1.TemporalConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: testNamespace,
				},
				Spec: temporaliov1alpha1.TemporalConnectionSpec{
					HostPort: ts.GetFrontendHostPort(),
				},
			}
			if err := k8sClient.Create(ctx, conn); err != nil {
				t.Fatalf("failed to create TemporalConnection %s: %v", name, err)
			}

			twd := testhelpers.NewTemporalWorkerDeploymentBuilder().
				WithManualStrategy().
				WithTargetTemplate("v1.0").
				WithName(name).
				WithNamespace(testNamespace).
				WithTemporalConnection(name).
				WithTemporalNamespace(ts.GetDefaultNamespace()).
				Build()
			if err := k8sClient.Create(ctx, twd); err != nil {
				t.Fatalf("failed to create TWD %s: %v", name, err)
			}
			twds[i] = twd
		}

		// With 10 TWDs reconciling concurrently every 1s and a 1 RPS limit, most will be
		// rate-limited almost immediately. Verify at least one surfaces the error with the
		// expected condition reason and "Rate limited" message (set by our ResourceExhausted
		// handler in worker_controller.go).
		eventually(t, 30*time.Second, time.Second, func() error {
			for _, twd := range twds {
				var fresh temporaliov1alpha1.TemporalWorkerDeployment
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      twd.Name,
					Namespace: twd.Namespace,
				}, &fresh); err != nil {
					return fmt.Errorf("get TWD %s: %w", twd.Name, err)
				}
				for _, c := range fresh.Status.Conditions {
					if c.Type == temporaliov1alpha1.ConditionProgressing &&
						c.Status == metav1.ConditionFalse &&
						c.Reason == temporaliov1alpha1.ReasonTemporalStateFetchFailed &&
						strings.Contains(c.Message, "Rate limited") {
						t.Logf("TWD %s confirmed rate-limited: %s", twd.Name, c.Message)
						return nil
					}
				}
			}
			return errors.New("no TWD has been rate-limited yet")
		})
	})
}
