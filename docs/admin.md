---
icon: lucide/shield-check
---

# Admin Space

As an admin (trainer, speaker, ...), you have access to the admin space to manage your labs:

* [x] Create a new lab
* [x] Define multiple Coder templates per lab (students get one workspace per template)
* [x] Dry run (preview) a lab before creating it
* [x] Set/update credentials for the cloud providers
* [x] Manage your labs
    * [x] See logs
    * [x] Retrieve Coder admin credentials (URL, email, password) for completed labs
    * [x] Delete a lab
    * [x] Recreate a destroyed lab with the same configuration
    * [x] List workspaces
    * [x] Delete workspaces (one by one or in bulk)
    * [x] Retry a failing lab installation
* [x] View student feedback per lab (rating, difficulty, comments)

![Admin header](screens/admin.png)

## Create a new lab

First you need to choose how to provide the Kubernetes cluster:

* [x] [Create New Infrastructure](#on-ovhcloud) — Provision a new cluster on a cloud provider (OVHcloud)
* [x] [Use Existing Cluster](#use-existing-cluster) — Provide a kubeconfig for an existing Kubernetes cluster

![Infrastructure selection](screens/infra-choice.png){width=350}

### Use Existing Cluster

When you choose **Use Existing Cluster**, EasyLab skips cloud provider provisioning and uses your own Kubernetes cluster. This is useful when you already have a cluster (e.g. from your organization, a local dev environment, or another cloud provider).

**What you need to provide:**

* **Kubeconfig** — Either:
    * Upload a `.yaml` or `.yml` kubeconfig file, or
    * Paste the kubeconfig YAML content directly

**What is skipped:**

* No cloud provider credentials required
* No network, cluster, or node pool configuration
* The wizard goes directly to Coder setup and template selection

The kubeconfig must have sufficient permissions to create namespaces and deploy Helm releases (Coder, PostgreSQL) in the cluster.

### On OVHcloud (Create New Infrastructure)

When creating new infrastructure, you choose OVHcloud as the cloud provider. Most of the configuration is preconfigured; you only need to select the ID for the private network.

??? info "Others parameters can be overridden if needed"

    | Category | Parameter                       | Description                                              |
    |----------|----------------------------------|---------------------------------------------------------|
    | Network | | |
    | | Gateway Name             | The name of the network gateway                          |
    | | Gateway Model            | The model of the network gateway                         |
    | | Private Network Name     | The name of the network private network                  |
    | | Region                   | The region of the network                                |
    | | Mask                     | The mask of the network                                  |
    | Node Pool | | |
    | | Name                   | The name of the node pool                                |
    | | Flavor                 | The flavor of the node pool                              |
    | | Desired Node Count     | The desired number of nodes in the node pool             |
    | | Min Node Count         | The minimum number of nodes in the node pool             |
    | | Max Node Count         | The maximum number of nodes in the node pool             |

### Setup Coder instance

You need to setup the secrets for the Coder instance:

* [x] Coder Admin Password
* [x] Coder Db Password

??? info "Others parameters can be overridden if needed"

    * `Coder Admin Email`: The email of the coder admin
    * `Coder Version`: The version of the coder
    * `Coder Db User`: The user of the coder database
    * `Coder Db Password`: The password of the coder database
    * `Coder Db Name`: The name of the coder database
    * `Coder Template Name`: The name of the coder template

#### Workspace Lifetime

Set **Workspace Lifetime (hours)** to automatically delete student workspaces after a given duration. The server checks every 5 minutes and deletes any workspace whose creation time exceeds the configured limit.

Leave the field empty or set it to `0` to disable automatic cleanup (workspaces persist until deleted manually).

Then, you need to define **one or more** [Coder templates](https://coder.com/docs/admin/templates){target="_blank"} for the lab. Each template is a different workspace type (e.g. Docker, Go, Node) that students can choose when requesting a workspace.

![Coder template selection](screens/templates.png)

Use **Add Template** to define additional templates. For each template you can:

* **Template name** — Name shown in Coder and in the student template selector.
* **Source** — Either upload a file or use a Git repository.

**Upload:** a zip file containing the template and documentation, or a single `.tf` file.

**Git:** provide the repository URL, optional folder path, and optional branch (default `main`).

At least one template is required. Students can request **one workspace per template** within a lab, so multiple templates let them get multiple workspaces in the same environment (e.g. one Docker workspace and one Go workspace).

### Template Variables

Coder templates can define Terraform `variable` blocks that need values at installation time. EasyLab supports setting these variables per template.

**Detect Variables** — Click the **Detect Variables** button on a template row to automatically scan the template source (uploaded file or Git repository) for Terraform variable definitions. EasyLab parses the `.tf` files, extracts all `variable` blocks, and shows each one as a name/value pair pre-filled with its default value (if any).

**Manual entry** — Click **+ Add Variable** to manually add a variable name and value. This is useful when you already know the variables your template expects.

Required variables (those without a default value in the `.tf` source) must be given a value before the template can be installed successfully. If a required variable is left empty, Coder will reject the template version during provisioning.

## Dry run (preview before create)

Before creating a lab, you can run a **dry run** to preview what Pulumi would do without actually provisioning resources. This is useful to validate configuration and catch errors early.

1. Complete the lab creation wizard up to the final step.
2. Click **Dry Run** instead of **Create Lab**. EasyLab runs `pulumi preview` and shows the planned changes.
3. If the dry run succeeds, the job appears in the labs list with status **dry-run-completed** (🔍).
4. From the labs list, you can then **Create Lab** on that job to perform the real deployment with the same configuration.

Dry-run jobs do not create any cloud or Kubernetes resources; only real runs do.

## Azure AD student authentication (optional)

EasyLab can delegate student login to Azure AD (Microsoft Entra ID). When enabled, a **Sign in with Microsoft** button appears at the top of the student login page. Any account in the configured tenant is accepted — no allow-list is required.

### How it works

1. The student clicks **Sign in with Microsoft** and is redirected to Microsoft's login page.
2. After a successful Microsoft login, EasyLab creates a local session using the student's email address.
3. When the student first requests a workspace, a Coder account is provisioned automatically using that email address and a server-generated password — identical to the password-based flow. Azure AD is never configured on Coder itself.

### Setup — Azure App Registration

1. In the [Azure portal](https://portal.azure.com), go to **Microsoft Entra ID → App registrations → New registration**.
2. Set **Redirect URI** to `https://<your-easylab-host>/student/auth/azure/callback` (Web platform).
3. Under **Certificates & secrets**, create a new client secret and copy its value immediately.
4. Note the **Application (client) ID** and **Directory (tenant) ID** from the Overview page.
5. Under **API permissions**, ensure the following delegated permissions are granted: `openid`, `email`, `profile`.

### Configuration — environment variables

Set the following three variables at startup (in your `.env` file or as Docker environment variables):

| Variable | Description |
|---|---|
| `AZURE_AD_CLIENT_ID` | Application (client) ID of the app registration |
| `AZURE_AD_CLIENT_SECRET` | Client secret value created in the app registration |
| `AZURE_AD_TENANT_ID` | Directory (tenant) ID |

If any of the three variables is absent, Azure AD login is disabled and only the password form is shown.

!!! note
    These are separate from the Azure infrastructure credentials (`AZURE_CLIENT_ID` / `AZURE_CLIENT_SECRET` / `AZURE_TENANT_ID`) used for AKS provisioning. You may use a dedicated app registration for student SSO.

## Provider credentials

Cloud provider credentials and options are accessed from the **Provider** dropdown in the header. It contains two entries:

* **OVH** — opens the OVH configuration page (`/admin/ovh-options`)
* **Azure** — opens the Azure configuration page (`/admin/azure-options`)

Each provider page has two tabs:

* **Credentials** — enter and save the API credentials for the provider. Credentials are stored in memory only and are cleared on application restart. When using **Use Existing Cluster**, no cloud credentials are required.
* **Options** — configure available regions and compute flavors/VM sizes for the lab creation wizard. Use **Refresh** to fetch the latest data from the provider API.

For OVHcloud-specific setup, see [OVHcloud configuration](ovhcloud.md). For Azure-specific setup, see [Azure configuration](azure.md).

## Manage your labs

Clicking on the `Labs` button in the header will redirect you to the labs list page. The **Provider** dropdown is available on every admin page and navigates directly to the OVH or Azure configuration page.

![Lab Info](screens/lab-info.png){width=350}

You can see all the labs you have created with following information:

* **Status** — created, running, completed, failed, destroyed, or dry-run-completed (preview-only)
* **Creation date**
* **Type** — Real run (🚀) or Dry run (🔍)
* **Access to the creation logs**
* **Access to the kubeconfig file** (for completed labs)
* **Retrieve Coder credentials** — For completed labs, a **Coder admin credentials** button opens a modal with the Coder URL, admin email, and admin password. You can copy each value or show/hide the password. Use these to sign in to the Coder instance for that lab.
* **Actions** — Destroy a lab; **Recreate** a destroyed lab with the same configuration (same Coder template, options, etc.)
* **List of workspaces** created for this lab — delete workspaces one by one or in bulk

![Lab Workspaces](screens/list-workspaces.png){width=350}

## Student Feedback

EasyLab collects feedback from students after each lab session. Admins can review the aggregated results per lab.

### Viewing feedback

Navigate to **Feedback** in the admin header. Select a lab from the dropdown and click **View Feedback**.

The page shows:

* **Response count** — total number of feedback submissions for the selected lab
* **Average rating** — mean star rating across all submissions
* **Individual entries** — one card per submission, displaying:
    * Star rating (1–5)
    * Difficulty level (Too Easy / A Bit Easy / Just Right / Challenging / Too Hard)
    * Free-text comment (if provided)
    * Submission date and time

If no feedback has been submitted yet, an empty state is displayed.

![Admin feedback](screens/feedbacks.png){width=45%}