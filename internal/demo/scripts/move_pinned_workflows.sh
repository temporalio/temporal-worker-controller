#!/bin/bash

# Moves pinned workflows that are running on version a to version b.
# Usage example, moving from v1 to v2:
#
#  $ move_pinned_workflows.sh staging/helloworld v1 v2

temporal workflow update-options \
  --query "TemporalWorkerDeploymentVersion='$1:$2' AND ExecutionStatus='Running'" \
  --versioning-override-behavior pinned \
  --versioning-override-deployment-name "$1" \
  --versioning-override-build-id "$3"

# Get the current temporal namespace
NAMESPACE=$(temporal env get -k namespace -o json | jq -r '.[0].value')

# URL encode the deployment version and build IDs for the query
SOURCE_BUILD_ID_ENCODED=$(printf '%s' "versioned:$2" | sed 's/:/%3A/g')
TARGET_DEPLOYMENT_VERSION_ENCODED=$(printf '%s' "$1:$3" | sed 's/:/%3A/g; s/\//%2F/g')

echo ""
echo "You can view the status of the impacted workflows at:"
echo "https://cloud.temporal.io/namespaces/${NAMESPACE}/workflows?query=BuildIds+IN+%28%22${SOURCE_BUILD_ID_ENCODED}%22%29+AND+TemporalWorkerDeploymentVersion+%3D+%22${TARGET_DEPLOYMENT_VERSION_ENCODED}%22"
