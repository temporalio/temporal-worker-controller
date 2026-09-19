package planner

import (
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
)

func TestGetVersionConfigDiff_NoRollbackWhenTargetIsAlreadyCurrent(t *testing.T) {
	testCases := []struct {
		name         string
		currentSince time.Duration
	}{
		{
			name:         "within rollback window",
			currentSince: 5 * time.Minute,
		},
		{
			name:         "beyond rollback window",
			currentSince: defaults.RollbackMaxVersionAge + 30*time.Minute,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var logLines []string
			logger := funcr.New(func(prefix, args string) {
				logLines = append(logLines, prefix+" "+args)
			}, funcr.Options{})

			lastCurrent := time.Now().Add(-tc.currentSince)
			version := temporaliov1alpha1.BaseWorkerDeploymentVersion{
				BuildID:      "build-a",
				Status:       temporaliov1alpha1.VersionStatusCurrent,
				HealthySince: &metav1.Time{Time: lastCurrent},
			}
			status := &temporaliov1alpha1.WorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: version,
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: version,
				},
				VersionConflictToken: []byte("token"),
			}
			state := &temporal.TemporalWorkerState{
				Versions: map[string]*temporal.VersionInfo{
					"build-a": {
						BuildID:         "build-a",
						LastCurrentTime: &lastCurrent,
						Status:          temporaliov1alpha1.VersionStatusCurrent,
					},
				},
			}
			config := &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Strategy: temporaliov1alpha1.UpdateProgressive,
					Steps: []temporaliov1alpha1.RolloutStep{
						{RampPercentage: 1, PauseDuration: metav1.Duration{Duration: time.Minute}},
					},
				},
			}

			versionConfig := getVersionConfigDiff(logger, status, state, config)

			assert.Nil(t, versionConfig, "target version is already current, so nothing should change")
			assert.Empty(t, logLines, "a steady-state current version is not a rollback and should not be logged as one")
		})
	}
}
