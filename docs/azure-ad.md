---
icon: lucide/key
title: Azure AD
---
# Azure AD authentication (optional)

EasyLab can delegate login to Azure AD (Microsoft Entra ID) for both **students** and **admins**.

---

## Student authentication

When enabled, a **Sign in with Microsoft** button appears on the student login page. Any account in the configured tenant is accepted — no allow-list is required.

### How it works

1. The student clicks **Sign in with Microsoft** and is redirected to Microsoft's login page.
2. After a successful Microsoft login, EasyLab creates a local session using the student's email address.
3. When the student first requests a workspace, the workspace is provisioned automatically for that email address with a server-generated access secret — identical to the password-based flow. Azure AD is never configured on the workspaces themselves.

![Student Microsoft login](screens/student-login-microsoft.png){ width=300 }

---

## Admin authentication

When an **Admin Group ID** is configured, a **Sign in with Microsoft** button also appears on the admin login page (`/login`). Only users who are **direct members** of the specified Azure AD group are granted admin access.

### How it works

1. The admin clicks **Sign in with Microsoft** and is redirected to Microsoft's login page.
2. After the Microsoft login, EasyLab calls the Microsoft Graph API (`/me/memberOf`) using the access token to verify the user is a direct member of the required group.
3. If the user is in the group, a normal admin session is created and the user is redirected to `/admin`. Otherwise, login is rejected with an error.

The admin password login remains available alongside Microsoft login — both methods work simultaneously.

![Admin Microsoft login](screens/admin-login-microsoft.png){ width=300 }

Once an **Admin Group ID** is saved, an additional option appears: **Disable password login for admins**. When this checkbox is enabled, the admin password form is hidden and admins can only log in via Microsoft. Has no effect if the Admin Group ID is not set.

---

## Setup — Azure App Registration

The same Azure AD app registration is used for both student and admin login.

1. In the [Azure portal](https://portal.azure.com), go to **Microsoft Entra ID → App registrations → New registration**.
2. Add **Redirect URIs** for both student and admin callbacks (Web platform):
    - `https://<your-easylab-host>/student/auth/azure/callback`
    - `https://<your-easylab-host>/admin/auth/azure/callback`
3. Under **Certificates & secrets**, create a new client secret and copy its value immediately.
4. Note the **Application (client) ID** and **Directory (tenant) ID** from the Overview page.
5. Under **API permissions**, ensure the following delegated permissions are granted: `openid`, `email`, `profile`, `User.Read`.
6. To enable admin login, find the **Object ID** of the Azure AD group whose members should be admins (under **Microsoft Entra ID → Groups → your group → Overview**).

## Configuration — admin UI (recommended)

The easiest way to configure Azure AD is through the admin interface:

1. Go to **Provider → Azure** in the header (or navigate to `/admin/azure-options`).
2. Select the **Credentials** tab.
3. Scroll down to the **Azure AD — Student Login (OAuth)** section.
4. Enter the **Application (Client) ID**, **Client Secret**, and **Directory (Tenant) ID**.
5. Optionally, enter the **Admin Group ID** (Azure AD group Object ID) to enable admin Microsoft login.
6. Click **Save Azure AD Config**.

The configuration is persisted to disk and takes effect immediately — no restart required. Click **Disable Azure AD Login** (visible when a client ID is saved) to clear the configuration.

![Azure AD configuration form](screens/azure-ad-config.png){width=700}

Once a client ID is saved, two additional options appear under **Login restrictions**:

* **Disable password login for students** — hides the student password form; students can only authenticate via Microsoft.
* **Disable password login for admins** — hides the admin password form on `/login`; only visible when an Admin Group ID is also set. Admins can only log in via Microsoft.

Neither option has any effect if Azure AD is not configured.

## Configuration — environment variables (alternative)

You can also set the following variables at startup (in your `.env` file or as Docker environment variables):

| Variable | Description |
|---|---|
| `AZURE_AD_CLIENT_ID` | Application (client) ID of the app registration |
| `AZURE_AD_CLIENT_SECRET` | Client secret value created in the app registration |
| `AZURE_AD_TENANT_ID` | Directory (tenant) ID |
| `AZURE_AD_ADMIN_GROUP_ID` | Azure AD group Object ID whose direct members are allowed as admins (optional) |

If any of the first three variables is absent and no UI configuration is saved, Azure AD login is disabled for both students and admins.

!!! note
    UI-saved configuration takes precedence over environment variables if both are present at startup. These are also separate from the Azure infrastructure credentials (`AZURE_CLIENT_ID` / `AZURE_CLIENT_SECRET` / `AZURE_TENANT_ID`) used for AKS provisioning.