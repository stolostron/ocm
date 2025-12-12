#!/bin/bash

# Script to fetch and update midstream backplane branches
# Usage: ./update-midstream-branches.sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
REMOTE="midstream"
BRANCHES=("backplane-2.7" "backplane-2.8" "backplane-2.9" "backplane-2.10")

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Updating Midstream Backplane Branches${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Step 1: Fetch all branches from midstream
echo -e "${YELLOW}📥 Fetching branches from ${REMOTE}...${NC}"
git fetch ${REMOTE} ${BRANCHES[@]}
echo ""

# Step 2: Update each branch
for BRANCH in "${BRANCHES[@]}"; do
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}Processing: ${REMOTE}/${BRANCH}${NC}"
    echo -e "${BLUE}========================================${NC}"

    # Checkout the branch
    echo -e "${YELLOW}🔀 Checking out ${REMOTE}/${BRANCH}...${NC}"
    git checkout ${REMOTE}/${BRANCH} 2>&1 | grep -v "warning: refname" || true

    # Pull latest changes
    echo -e "${YELLOW}⬇️  Pulling latest changes...${NC}"
    PULL_OUTPUT=$(git pull ${REMOTE} ${BRANCH} 2>&1)

    # Check if there were updates
    if echo "$PULL_OUTPUT" | grep -q "Already up to date"; then
        echo -e "${GREEN}✅ Already up to date${NC}"
    elif echo "$PULL_OUTPUT" | grep -q "Fast-forward"; then
        echo -e "${GREEN}✅ Updated (Fast-forward)${NC}"
        # Show number of files changed
        FILES_CHANGED=$(echo "$PULL_OUTPUT" | grep "files changed" || echo "")
        if [ -n "$FILES_CHANGED" ]; then
            echo -e "${GREEN}   $FILES_CHANGED${NC}"
        fi
    else
        echo -e "${GREEN}✅ Updated${NC}"
    fi

    # Show latest commit
    echo -e "${YELLOW}📝 Latest commit:${NC}"
    git log -1 --oneline
    echo ""
done

# Step 3: Summary
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Summary${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

CURRENT_BRANCH=$(git branch --show-current)
echo -e "${GREEN}✅ All branches updated successfully!${NC}"
echo -e "${YELLOW}📍 Current branch: ${CURRENT_BRANCH}${NC}"
echo ""

echo -e "${BLUE}Branch status:${NC}"
for BRANCH in "${BRANCHES[@]}"; do
    COMMIT=$(git log -1 --oneline ${REMOTE}/${BRANCH} 2>/dev/null || echo "N/A")
    echo -e "  • ${REMOTE}/${BRANCH}: ${COMMIT}"
done

echo ""
echo -e "${GREEN}Done!${NC}"
