#!/bin/bash
set -e

echo "=== Pre-deploy hook: Updating Chart.yaml appVersion ==="
echo "Available environment variables:"
env | grep -E 'IMAGE|SKAFFOLD|TAG' | sort || echo "No IMAGE/SKAFFOLD/TAG variables found"

# Try to extract tag from process list (skaffold deploy --tag xxx)
SKAFFOLD_TAG=$(ps aux | grep 'skaffold deploy' | grep -o '\-\-tag [^ ]*' | awk '{print $2}' | head -1)

echo ""
echo "Extracted tag from process: $SKAFFOLD_TAG"

echo ""
echo "Current Chart.yaml appVersion:"
grep "appVersion:" internal/demo/helloworld/helm/helloworld/Chart.yaml

# Try to extract tag from command line args if available
if [ $# -gt 0 ]; then
    TAG=$1
    echo "Using tag from argument: $TAG"
    sed -i '' "s/appVersion: .*/appVersion: \"$TAG\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
elif [ -n "$SKAFFOLD_TAG" ]; then
    echo "Using SKAFFOLD_TAG from process: $SKAFFOLD_TAG"
    sed -i '' "s/appVersion: .*/appVersion: \"$SKAFFOLD_TAG\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
elif [ -n "$SKAFFOLD_IMAGE_TAG" ]; then
    echo "Using SKAFFOLD_IMAGE_TAG: $SKAFFOLD_IMAGE_TAG"
    sed -i '' "s/appVersion: .*/appVersion: \"$SKAFFOLD_IMAGE_TAG\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
elif [ -n "$IMAGE_TAG_helloworld" ]; then
    echo "Using IMAGE_TAG_helloworld: $IMAGE_TAG_helloworld"
    sed -i '' "s/appVersion: .*/appVersion: \"$IMAGE_TAG_helloworld\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
else
    echo "No tag found, keeping current appVersion"
fi

echo ""
echo "Updated Chart.yaml appVersion:"
grep "appVersion:" internal/demo/helloworld/helm/helloworld/Chart.yaml
