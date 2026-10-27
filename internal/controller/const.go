// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

// Event reason constants for WorkerDeployment.
//
// These strings appear in Kubernetes Event objects (kubectl get events) and are
// internal to the controller's implementation. They are not part of the CRD
// status API and may change between releases. Do not write alerting or automation
// that depends on these strings.
const (
	ReasonPlanGenerationFailed       = "PlanGenerationFailed"
	ReasonPlanExecutionFailed        = "PlanExecutionFailed"
	ReasonDeploymentCreateFailed     = "DeploymentCreateFailed"
	ReasonDeploymentDeleteFailed     = "DeploymentDeleteFailed"
	ReasonDeploymentScaleFailed      = "DeploymentScaleFailed"
	ReasonDeploymentUpdateFailed     = "DeploymentUpdateFailed"
	ReasonTestWorkflowStartFailed    = "TestWorkflowStartFailed"
	ReasonVersionPromotionFailed     = "VersionPromotionFailed"
	ReasonMetadataUpdateFailed       = "MetadataUpdateFailed"
	ReasonManagerIdentityClaimFailed = "ManagerIdentityClaimFailed"
	ReasonGateWorkflowFailed         = "GateWorkflowFailed"
)

const (
	IdentityMetadataKey = "temporal.io/controller"
	VersionMetadataKey  = "temporal.io/controller-version"

	serverDeleteVersionIdentity = "try-delete-for-add-version"
)
