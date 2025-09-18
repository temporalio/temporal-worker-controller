#!/bin/bash
# Checkout a commit by searching for a message pattern

if [ -z "$1" ]; then
    echo "Usage: $0 <commit-message-pattern>"
    echo "Example: $0 'Fix progressive rollout'"
    echo "Example: $0 'Add debug logging'"
    exit 1
fi

COMMIT=$(git log --oneline --grep="$1" -1 | awk '{print $1}')

if [ -z "$COMMIT" ]; then
    echo "No commit found matching: $1"
    exit 1
fi

echo "Found commit: $COMMIT"
git log -1 --pretty=format:"%h - %s (%an, %ar)" $COMMIT
echo ""
echo "Checking out..."
git checkout $COMMIT