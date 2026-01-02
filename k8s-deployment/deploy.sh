#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Deploying Lab-as-Code to Kubernetes${NC}"

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
# docker build -t lab-as-code:latest ../
# docker push lab-as-code:latest

echo -e "${BLUE}📦 Deploying to Kubernetes...${NC}"

# Apply all manifests
kubectl apply -k .

echo -e "${GREEN}✅ Deployment completed!${NC}"

# Wait for deployment to be ready
echo -e "${BLUE}⏳ Waiting for deployment to be ready...${NC}"
kubectl wait --for=condition=available --timeout=300s deployment/lab-as-code -n lab-as-code

# Get service information
echo -e "${GREEN}🎉 Deployment successful!${NC}"
echo ""
echo -e "${BLUE}Service Information:${NC}"
kubectl get svc lab-as-code-service -n lab-as-code
echo ""
echo -e "${BLUE}Pod Status:${NC}"
kubectl get pods -n lab-as-code -l app=lab-as-code
echo ""
echo -e "${YELLOW}📝 Next steps:${NC}"
echo "1. Update the ingress host in ingress.yaml with your domain"
echo "2. Configure DNS to point to your ingress controller"
echo "3. Update secrets with your actual OVH credentials:"
echo "   kubectl edit secret lab-as-code-secrets -n lab-as-code"
echo ""
echo -e "${BLUE}Application will be available at:${NC}"
echo "http://lab-as-code-service.lab-as-code.svc.cluster.local"
echo "(or through ingress if configured)"
