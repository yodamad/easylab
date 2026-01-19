#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Deploying EasyLab to Kubernetes${NC}"

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}❌ kubectl is not installed or not in PATH${NC}"
    exit 1
fi

# Check if we're connected to a cluster
if ! kubectl cluster-info &> /dev/null; then
    echo -e "${RED}❌ Not connected to a Kubernetes cluster${NC}"
    exit 1
fi

echo -e "${YELLOW}⚠️  Warning: This will deploy to the current kubectl context:${NC}"
kubectl config current-context
echo ""

read -p "Continue? (y/N): " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${RED}❌ Deployment cancelled${NC}"
    exit 1
fi

# Build and push Docker image (optional - uncomment if you want to build)
# echo -e "${BLUE}🔨 Building Docker image...${NC}"
# docker build -t easylab:latest ../
# docker push easylab:latest

echo -e "${BLUE}📦 Deploying to Kubernetes...${NC}"

# Apply all manifests
kubectl apply -k .

echo -e "${GREEN}✅ Deployment completed!${NC}"

# Wait for deployment to be ready
echo -e "${BLUE}⏳ Waiting for deployment to be ready...${NC}"
kubectl wait --for=condition=available --timeout=300s deployment/easylab -n easylab

# Get service information
echo -e "${GREEN}🎉 Deployment successful!${NC}"
echo ""
echo -e "${BLUE}Service Information:${NC}"
kubectl get svc easylab-service -n easylab
echo ""
echo -e "${BLUE}Pod Status:${NC}"
kubectl get pods -n easylab -l app=easylab
echo ""
echo -e "${YELLOW}📝 Next steps:${NC}"
echo "1. Update the ingress host in ingress.yaml with your domain"
echo "2. Configure DNS to point to your ingress controller"
echo "3. Update secrets with your actual OVH credentials:"
echo "   kubectl edit secret easylab-secrets -n easylab"
echo ""
echo -e "${BLUE}Application will be available at:${NC}"
echo "http://easylab-service.easylab.svc.cluster.local"
echo "(or through ingress if configured)"
