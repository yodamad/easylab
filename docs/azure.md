# Azure Setup Guide

This guide explains how to configure EasyLab to provision lab environments on Microsoft Azure using AKS (Azure Kubernetes Service).

## Overview

EasyLab supports Azure as a cloud provider, creating:

- An **Azure Resource Group** per lab (`easylab-{stackName}`)
- An **AKS cluster** with a node pool in the selected region
- A **Coder** deployment on the cluster

Networking is AKS-managed (Kubenet default). No custom VNet or subnet configuration is required.

## Prerequisites

- An Azure subscription
- Permissions to create resource groups, AKS clusters, and assign roles

### RBAC scope (important)

EasyLab calls **subscription-level** Azure Resource Manager APIs for regions (`Microsoft.Resources/subscriptions/locations/read`) and loads VM sizes for a chosen region (`Microsoft.Compute/locations/virtualMachineSizes/read`). Your service principal needs a role assignment **on the subscription** (`/subscriptions/<SUBSCRIPTION_ID>`), not only on a resource group.

- **Minimum for “Refresh from Azure” / region dropdowns:** **Reader** at subscription scope (includes read on locations and discovery).
- **For lab provisioning (AKS, networking, etc.):** **Contributor** at subscription scope (as in the command below), or an equivalent custom role that includes both read and write where you deploy.

If you see an error like *does not have authorization to perform action `Microsoft.Resources/subscriptions/locations/read`*, grant **Reader** or **Contributor** at the **subscription** for that app registration, then save credentials again and retry **Refresh from Azure**.

```bash
# Example: add subscription-scoped Reader for least privilege on discovery APIs only
az role assignment create \
  --assignee <APP_ID_OR_OBJECT_ID> \
  --role Reader \
  --scope /subscriptions/<SUBSCRIPTION_ID>

# If you already use Contributor on the subscription, you do not need a separate Reader assignment.
```

## Create a Service Principal

EasyLab authenticates to Azure using a Service Principal with a client secret.

```bash
# Login to Azure CLI
az login

# Create a Service Principal with Contributor role on your subscription
az ad sp create-for-rbac \
  --name "easylab-sp" \
  --role Contributor \
  --scopes /subscriptions/<SUBSCRIPTION_ID>
```

The command outputs JSON like:

```json
{
  "appId": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "displayName": "easylab-sp",
  "password": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "tenant": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
}
```

Map these values to EasyLab credentials:

| Azure output | EasyLab field |
|---|---|
| `appId` | Client ID |
| `password` | Client Secret |
| `tenant` | Tenant ID |
| (your subscription) | Subscription ID |

## Required Environment Variables

You can pre-configure credentials via environment variables (loaded at startup):

```env
AZURE_CLIENT_ID=<appId>
AZURE_CLIENT_SECRET=<password>
AZURE_TENANT_ID=<tenant>
AZURE_SUBSCRIPTION_ID=<subscription-id>
```

Or use the `--env-file` flag:

```bash
./easylab --env-file /path/to/.env
```

## Configure Credentials in the UI

1. Navigate to **Credentials** in the EasyLab admin UI
2. Select **Microsoft Azure** from the provider dropdown
3. Enter your Service Principal credentials:
   - **Client ID** — the `appId` value
   - **Client Secret** — the `password` value
   - **Tenant ID** — the `tenant` value
   - **Subscription ID** — your Azure subscription ID
4. Click **Save Credentials**

## Configure Azure Options

After saving credentials, set up default regions and VM sizes:

1. Navigate to **Azure → Options** (or `/admin/azure-options`)
2. Click **Refresh from Azure** to load the subscription’s regions (locations)
3. Enable the regions you want to offer in the lab creation form
4. Set a default region
5. Expand each region to load VM sizes from Azure; enable the VM sizes you want per region
6. Click **Save Options**

## Create a Lab on Azure

1. Go to **New Lab**
2. Select **Create New Infrastructure**
3. Choose **Microsoft Azure** as the cloud provider
4. In the **Azure Region** step, choose the target region — VM sizes for the node pool load after a region is selected
5. Configure the node pool (name, VM size, node counts)
6. Complete the Coder configuration steps
7. Click **Create Lab**

EasyLab will:
- Create resource group `easylab-{stackName}` in the selected region
- Provision an AKS cluster with the configured node pool
- Deploy Coder via Helm
- Return the Coder URL when complete

## Cleanup

Destroying a lab via the EasyLab UI runs `pulumi destroy`, which removes:

- The AKS cluster and all node pools
- The resource group and all contained resources

No manual cleanup is needed.
