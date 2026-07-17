#!/bin/bash

# Build and test script for plundrio Docker image

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
IMAGE_NAME="plundrio-fixed"
DOCKER_USERNAME="${DOCKER_USERNAME:-your-username}"

echo -e "${GREEN}🚀 Building plundrio Docker image${NC}"

# Build for local platform first (faster for testing)
echo -e "${YELLOW}📦 Building for local platform...${NC}"
docker build -t "${DOCKER_USERNAME}/${IMAGE_NAME}:local" .

# Test the local build
echo -e "${YELLOW}🧪 Testing local build...${NC}"
docker run --rm "${DOCKER_USERNAME}/${IMAGE_NAME}:local" --version

# Build multi-platform images (requires buildx)
echo -e "${YELLOW}🏗️  Building multi-platform images...${NC}"
docker buildx create --use --name multiarch-builder 2>/dev/null || docker buildx use multiarch-builder

# Build and push (or just build for local testing)
if [ "$1" = "push" ]; then
    echo -e "${YELLOW}🚀 Building and pushing multi-platform images...${NC}"
    docker buildx build \
        --platform linux/amd64,linux/arm64 \
        --tag "${DOCKER_USERNAME}/${IMAGE_NAME}:latest" \
        --tag "${DOCKER_USERNAME}/${IMAGE_NAME}:$(date +%Y%m%d)" \
        --push \
        .
    echo -e "${GREEN}✅ Images pushed to Docker Hub!${NC}"
else
    echo -e "${YELLOW}🏗️  Building multi-platform images (local only)...${NC}"
    docker buildx build \
        --platform linux/amd64,linux/arm64 \
        --tag "${DOCKER_USERNAME}/${IMAGE_NAME}:latest" \
        .
    echo -e "${GREEN}✅ Multi-platform images built successfully!${NC}"
    echo -e "${YELLOW}💡 Run '$0 push' to push to Docker Hub${NC}"
fi

echo -e "${GREEN}🎉 Done!${NC}"
echo -e "${YELLOW}📝 To use in your docker-compose.yml:${NC}"
echo -e "    image: ${DOCKER_USERNAME}/${IMAGE_NAME}:latest" 