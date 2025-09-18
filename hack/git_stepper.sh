#!/bin/bash
# Git commit stepper - navigate through commit history step by step

case "$1" in
    "start")
        if [ -z "$2" ]; then
            echo "Usage: $0 start <number-of-commits-back>"
            echo "Example: $0 start 5  # Go back 5 commits"
            exit 1
        fi
        git checkout HEAD~$2
        echo "Checked out HEAD~$2 ($(git rev-parse --short HEAD))"
        echo "Use '$0 next' to step forward or '$0 back' to step backward"
        ;;
    "next")
        CURRENT=$(git rev-parse HEAD)
        TARGET_BRANCH="jlegrone/demo-2"
        
        NEXT=$(git log --reverse --pretty=format:"%H" ${CURRENT}..origin/${TARGET_BRANCH} 2>/dev/null | head -1)
        
        if [ -z "$NEXT" ]; then
            echo "No more commits to step through. Checking out ${TARGET_BRANCH}..."
            git checkout ${TARGET_BRANCH}
            echo "Checked out branch ${TARGET_BRANCH} ($(git rev-parse --short HEAD))"
        else
            git checkout $NEXT
            echo "Stepped forward to $(git rev-parse --short HEAD)"
            echo "$(git log -1 --pretty=format:'%s')"
        fi
        ;;
    "back")
        git checkout HEAD~1
        echo "Stepped back to $(git rev-parse --short HEAD)"
        echo "$(git log -1 --pretty=format:'%s')"
        ;;
    "current")
        echo "Currently at: $(git rev-parse --short HEAD)"
        echo "$(git log -1 --pretty=format:'%s')"
        ;;
    "done")
        git checkout -
        echo "Returned to original branch"
        ;;
    *)
        echo "Usage:"
        echo "  $0 start <n>  # Go back n commits"
        echo "  $0 next       # Step forward one commit"
        echo "  $0 back       # Step back one commit"
        echo "  $0 current    # Show current commit"
        echo "  $0 done       # Return to original branch"
        ;;
esac