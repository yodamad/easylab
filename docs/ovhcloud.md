---
icon: lucide/cloud-check
title: OVHcloud
---
# OVHcloud configuration

This section is dedicated to the OVHcloud configuration.

## Configuration keys reference

All configuration keys related to OVHcloud are listed below. These are set via the EasyLab UI; keys used only when running Pulumi CLI directly are not documented here.

### API credentials (environment variables)

| Key | Description | Default |
|-----|-------------|---------|
| `OVH_APPLICATION_KEY` | OVHcloud application key | - |
| `OVH_APPLICATION_SECRET` | OVHcloud application secret | - |
| `OVH_CONSUMER_KEY` | OVHcloud consumer key | - |
| `OVH_SERVICE_NAME` | OVHcloud project/service name | - |
| `OVH_ENDPOINT` | OVHcloud API endpoint | `ovh-eu` |

### Pulumi provider config

| Key | Description | Default |
|-----|-------------|---------|
| `ovh:endpoint` | OVHcloud API endpoint (set automatically from `OVH_ENDPOINT`) | `ovh-eu` |

### Network infrastructure (Pulumi config: `network:*`)

| Key | Description | Required |
|-----|-------------|----------|
| `network:region` | OVHcloud region (e.g. `GRA11`, `SBG5`) | Yes |
| `network:gatewayName` | Name of the gateway | Yes |
| `network:gatewayModel` | Gateway model | Yes |
| `network:privateNetworkName` | Name of the private network | Yes |
| `network:networkId` | Network ID | Yes |
| `network:networkMask` | Network mask (e.g. `255.255.255.0`) | Yes |
| `network:networkStartIp` | Start IP of the subnet range | Yes |
| `network:networkEndIp` | End IP of the subnet range | Yes |

### Node pool (Pulumi config: `nodepool:*`)

| Key | Description |
|-----|-------------|
| `nodepool:name` | Node pool name |
| `nodepool:flavor` | OVHcloud instance flavor |
| `nodepool:desiredNodeCount` | Desired number of nodes |
| `nodepool:minNodeCount` | Minimum number of nodes |
| `nodepool:maxNodeCount` | Maximum number of nodes |

### Kubernetes (Pulumi config: `k8s:*`)

| Key | Description |
|-----|-------------|
| `k8s:clusterName` | Managed Kubernetes cluster name |

