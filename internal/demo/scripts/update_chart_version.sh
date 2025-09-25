#!/bin/bash
set -e

# Update Chart.yaml appVersion to match the deployed image tag
# Used as a Skaffold pre-deploy hook to keep Helm chart version in sync

if [ $# -gt 0 ]; then
    # Use tag from command line argument if provided
    sed -i '' "s/appVersion: .*/appVersion: \"$1\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
else
    # Extract tag from running skaffold deploy command
    TAG=$(ps aux | grep 'skaffold deploy' | grep -o '\-\-tag [^ ]*' | awk '{print $2}' | head -1)
    if [ -n "$TAG" ]; then
        sed -i '' "s/appVersion: .*/appVersion: \"$TAG\"/" internal/demo/helloworld/helm/helloworld/Chart.yaml
    fi
fi
