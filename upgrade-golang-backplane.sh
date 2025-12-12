#!/bin/bash

# Script to upgrade golang to 1.25 for all midstream backplane branches
# Usage: ./upgrade-golang-backplane.sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
BACKPLANE_VERSIONS=("2.7" "2.8" "2.9" "2.10")
MIDSTREAM_REMOTE="midstream"
MIDSTREAM_REPO="stolostron/ocm"
ORIGIN_REMOTE="origin"

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Golang 1.25 Upgrade for Backplane Branches${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Function to upgrade files for golang 1.25
upgrade_golang() {
    local version=$1
    echo -e "${YELLOW}📝 Applying golang 1.25 upgrade...${NC}"

    # Update GitHub workflows
    if [ -f .github/workflows/cloudevents-integration.yml ]; then
        sed -i "s/GO_VERSION: '1\.24'/GO_VERSION: '1.25'/g" .github/workflows/cloudevents-integration.yml
    fi
    if [ -f .github/workflows/e2e.yml ]; then
        sed -i "s/GO_VERSION: '1\.24'/GO_VERSION: '1.25'/g" .github/workflows/e2e.yml
    fi
    if [ -f .github/workflows/post.yml ]; then
        sed -i "s/GO_VERSION: '1\.24'/GO_VERSION: '1.25'/g" .github/workflows/post.yml
    fi
    if [ -f .github/workflows/pre.yml ]; then
        sed -i "s/GO_VERSION: '1\.24'/GO_VERSION: '1.25'/g" .github/workflows/pre.yml
    fi
    if [ -f .github/workflows/releaseimage.yml ]; then
        sed -i "s/GO_VERSION: '1\.24'/GO_VERSION: '1.25'/g" .github/workflows/releaseimage.yml
    fi

    # Update Dockerfiles (bullseye -> bookworm)
    for dockerfile in build/Dockerfile.addon build/Dockerfile.placement build/Dockerfile.registration build/Dockerfile.registration-operator build/Dockerfile.work; do
        if [ -f "$dockerfile" ]; then
            sed -i 's/FROM golang:1\.24-bullseye/FROM golang:1.25-bookworm/g' "$dockerfile"
        fi
    done

    # Update go.mod
    if [ -f go.mod ]; then
        sed -i 's/^go 1\.24\.0$/go 1.25.0/g' go.mod
    fi

    # Update development.md if exists
    if [ -f development.md ]; then
        sed -i 's/Go 1\.24\.0/Go 1.25.0/g' development.md
    fi

    echo -e "${GREEN}✅ Files updated${NC}"
}

# Process each backplane version
for VERSION in "${BACKPLANE_VERSIONS[@]}"; do
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}Processing backplane-${VERSION}${NC}"
    echo -e "${BLUE}========================================${NC}"

    MIDSTREAM_BRANCH="backplane-${VERSION}"
    NEW_BRANCH="br_golang25_backplane-${VERSION}"

    # Checkout midstream branch
    echo -e "${YELLOW}🔀 Checking out ${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}...${NC}"
    git checkout ${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH} 2>&1 | grep -v "warning: refname" || true

    # Create new branch
    echo -e "${YELLOW}🌱 Creating new branch: ${NEW_BRANCH}...${NC}"
    git checkout -b ${NEW_BRANCH}

    # Apply golang upgrade
    upgrade_golang ${VERSION}

    # Check for changes
    if git diff --quiet; then
        echo -e "${YELLOW}⚠️  No changes detected. Skipping commit.${NC}"
        git checkout ${MIDSTREAM_REMOTE}/${MIDSTREAM_BRANCH}
        git branch -D ${NEW_BRANCH}
        echo ""
        continue
    fi

    # Show changes
    echo -e "${YELLOW}📋 Changed files:${NC}"
    git status --short
    echo ""

    # Stage all changes
    echo -e "${YELLOW}➕ Staging changes...${NC}"
    git add -A

    # Commit with sign-off
    echo -e "${YELLOW}💾 Creating commit...${NC}"
    git commit -s -m "Upgrade to Go 1.25.0

Update Go version from 1.24.0 to 1.25.0 across:
- GitHub Actions workflows (cloudevents-integration, e2e, post, pre, releaseimage)
- All Dockerfiles using golang:1.25-bookworm (Debian 12)
- go.mod directive
- Development documentation

Note: Updated from bullseye to bookworm as Go 1.25 Docker images
no longer support Debian Bullseye (11).

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>"

    # Show commit
    echo -e "${GREEN}✅ Commit created:${NC}"
    git log -1 --oneline
    echo ""

    # Push to origin
    echo -e "${YELLOW}🚀 Pushing to ${ORIGIN_REMOTE}...${NC}"
    git push -u ${ORIGIN_REMOTE} ${NEW_BRANCH}
    echo -e "${GREEN}✅ Pushed to ${ORIGIN_REMOTE}/${NEW_BRANCH}${NC}"
    echo ""

    # Create PR to midstream
    echo -e "${YELLOW}📬 Creating PR to ${MIDSTREAM_REPO}...${NC}"
    PR_BODY="## Summary

Upgrade Go version from 1.24.0 to 1.25.0 for backplane-${VERSION}.

## Changes

This PR updates the Go version to 1.25.0 in the following locations:

### GitHub Actions Workflows
- \`.github/workflows/cloudevents-integration.yml\`
- \`.github/workflows/e2e.yml\`
- \`.github/workflows/post.yml\`
- \`.github/workflows/pre.yml\`
- \`.github/workflows/releaseimage.yml\`

### Dockerfiles
- \`build/Dockerfile.addon\`
- \`build/Dockerfile.placement\`
- \`build/Dockerfile.registration\`
- \`build/Dockerfile.registration-operator\`
- \`build/Dockerfile.work\`

**Note**: Updated from \`golang:1.24-bullseye\` to \`golang:1.25-bookworm\` as Go 1.25 Docker images no longer support Debian Bullseye (11).

### Go Module
- \`go.mod\` - Updated Go directive from \`1.24.0\` to \`1.25.0\`

### Documentation
- \`development.md\` - Updated prerequisite and technology references (if exists)

## Testing

- [ ] All GitHub Actions workflows will use Go 1.25.0
- [ ] All container images will be built with golang:1.25-bookworm
- [ ] Documentation accurately reflects the required Go version

## Related Issues

Part of the ongoing effort to keep OCM dependencies up to date.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"

    gh pr create \
        --repo ${MIDSTREAM_REPO} \
        --base ${MIDSTREAM_BRANCH} \
        --head haoqing0110:${NEW_BRANCH} \
        --title "[backplane-${VERSION}] Upgrade to Go 1.25.0" \
        --body "$PR_BODY" || {
        echo -e "${RED}❌ Failed to create PR. You may need to create it manually at:${NC}"
        echo -e "${YELLOW}https://github.com/${MIDSTREAM_REPO}/compare/${MIDSTREAM_BRANCH}...haoqing0110:OCM:${NEW_BRANCH}?expand=1${NC}"
    }

    echo ""
done

# Summary
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Summary${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo -e "${GREEN}✅ Golang 1.25 upgrade completed for all backplane branches!${NC}"
echo ""
echo -e "${BLUE}Branches created:${NC}"
for VERSION in "${BACKPLANE_VERSIONS[@]}"; do
    echo -e "  • br_golang25_backplane-${VERSION}"
done
echo ""
echo -e "${YELLOW}📍 PRs created (or ready to create manually) for:${NC}"
echo -e "   Repository: ${MIDSTREAM_REPO}"
for VERSION in "${BACKPLANE_VERSIONS[@]}"; do
    echo -e "   • backplane-${VERSION} ← haoqing0110:br_golang25_backplane-${VERSION}"
done
echo ""
echo -e "${GREEN}Done!${NC}"
