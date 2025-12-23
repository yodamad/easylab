# OVHcloud Gateway and Managed Kubernetes Infrastructure

This Pulumi project provisions infrastructure on OVHcloud including:
- A private network
- A gateway
- A managed Kubernetes cluster
- Configurable node pools

## Prerequisites

1. **Install Pulumi CLI**: Follow the [official installation guide](https://www.pulumi.com/docs/get-started/install/)
2. **Install Go**: Version 1.21 or later
3. **OVHcloud Account**: With an active project and API credentials

## OVHcloud API Credentials Setup

You need to configure OVHcloud API credentials. You can either:

### Option 1: Environment Variables (Recommended)
```bash
export OVH_ENDPOINT=ovh-eu          # or ovh-us, ovh-ca
export OVH_APPLICATION_KEY=your_application_key
export OVH_APPLICATION_SECRET=your_application_secret
export OVH_CONSUMER_KEY=your_consumer_key
```

### Option 2: Pulumi Configuration
```bash
pulumi config set ovh:endpoint ovh-eu
pulumi config set ovh:applicationKey your_application_key --secret
pulumi config set ovh:applicationSecret your_application_secret --secret
pulumi config set ovh:consumerKey your_consumer_key --secret
```

For instructions on obtaining OVHcloud API credentials, visit: https://www.pulumi.com/registry/packages/ovh/installation-configuration/

## Configuration

The project is highly configurable. Edit `Pulumi.dev.yaml` or use `pulumi config set` to configure:

### Required Configuration
- `ovhServiceName`: Your OVHcloud project ID (service name)

### Optional Configuration (with defaults)
- `location`: OVHcloud region (default: `GRA11`)
  - Common regions: `GRA11`, `SBG5`, `BHS5`, `WAW1`, `DE1`
- `nodePoolCount`: Number of node pools to create (default: `1`)
- `vmSize`: VM flavor size (default: `b2-7`)
  - Common flavors: `b2-7`, `b2-15`, `c2-7`, `c2-15`
- `gatewayModel`: Gateway model (default: `s`)
  - Options: `s` (small), `m` (medium), `l` (large)
- `minNodes`: Minimum nodes per pool (default: `1`)
- `maxNodes`: Maximum nodes per pool (default: `3`)
- `desiredNodes`: Desired nodes per pool (default: `1`)

### Example Configuration Commands

```bash
# Set OVHcloud project ID (required)
pulumi config set ovhServiceName "your-project-id"

# Configure location
pulumi config set location "SBG5"

# Configure node pools
pulumi config set nodePoolCount 3
pulumi config set vmSize "b2-15"

# Configure gateway
pulumi config set gatewayModel "m"

# Configure node pool scaling
pulumi config set minNodes 2
pulumi config set maxNodes 5
pulumi config set desiredNodes 3
```

## Installation

1. **Install dependencies**:
```bash
go mod tidy
```

2. **Initialize Pulumi stack** (if not already done):
```bash
pulumi stack init dev
```

3. **Configure your OVHcloud project ID**:
```bash
pulumi config set ovhServiceName "your-project-id"
```

## Deployment

1. **Preview changes**:
```bash
pulumi preview
```

2. **Deploy infrastructure**:
```bash
pulumi up
```

3. **Destroy infrastructure** (when done):
```bash
pulumi destroy
```

## Outputs

After deployment, the following outputs are available:
- `location`: The configured region
- `nodePoolCount`: Number of node pools created
- `vmSize`: VM flavor used
- `privateNetworkId`: ID of the created private network
- `gatewayId`: ID of the created gateway
- `kubeClusterId`: ID of the Kubernetes cluster
- `kubeClusterName`: Name of the Kubernetes cluster
- `nodePoolIds`: Array of node pool IDs
- `nodePool{N}Id`: Individual node pool IDs
- `nodePool{N}Name`: Individual node pool names

## Accessing Kubernetes

To get the kubeconfig for your cluster, you can use the OVHcloud console or API. The cluster is configured to use the private network with the gateway for routing.

## Project Structure

```
.
├── Pulumi.yaml          # Pulumi project configuration
├── Pulumi.dev.yaml      # Stack-specific configuration
├── go.mod               # Go module dependencies
├── main.go              # Main infrastructure code
├── .gitignore          # Git ignore rules
└── README.md           # This file
```

## Troubleshooting

### Common Issues

1. **Authentication Errors**: Ensure your OVHcloud API credentials are correctly configured
2. **Region Not Available**: Verify the region code is correct for your OVHcloud account
3. **Flavor Not Available**: Check available flavors in your region using the OVHcloud console
4. **Project ID**: Make sure you're using the correct project ID (service name)

### Getting Help

- [Pulumi Documentation](https://www.pulumi.com/docs/)
- [OVHcloud Pulumi Provider](https://www.pulumi.com/registry/packages/ovh/)
- [OVHcloud Documentation](https://docs.ovh.com/)

