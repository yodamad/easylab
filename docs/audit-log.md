---
icon: lucide/scroll-text
title: Audit Log
---
# Audit Log

EasyLab records a lightweight audit trail of lab, workspace, and credential actions: who did what, and when.

## Viewing the audit log

Navigate to **Audit Log** in the admin sidebar. The page lists the 200 most recent recorded actions, newest first:

* **Time** — when the action was recorded
* **Actor** — the email of the person who performed the action, or a generic label (see the identity caveat below)
* **Role** — `admin`, `student`, or `system`
* **Action** — e.g. `lab.create`, `workspace.delete`, `credentials.set`
* **Lab** — the affected lab's ID, when applicable
* **Detail** — a short, human-readable note (stack name, workspace name, or similar) — never a credential, kubeconfig, or secret

If nothing has been recorded yet, an empty state is displayed.

A single lab's own entries are also shown, pre-filtered, in the **Activity** section of
its [detail page](admin-lab-management.md#the-lab-detail-page) — handy when you only care about one
lab's history rather than scanning the global log.

## What's tracked

* Lab actions: create, dry run, launch, destroy, retry (with or without an edited configuration), recreate, delete, template upload, template removal, lifecycle edit, closing or reopening a lab to new students (`lab.disable` / `lab.enable`), closing or reopening one template (`lab.template_disable` / `lab.template_enable`, with the template name as detail)
* Workspace actions: student-initiated creation, student-initiated deletion (**Clear** / **Clear All** on the My Workspaces page), admin-initiated deletion (single, bulk, or as part of removing a template), an admin opening a student's workspace (`workspace.open`)
* Credential changes: saving OVH or Azure credentials (the credential values themselves are never recorded)
* [Credential storage](admin.md#credential-storage) actions: setting or changing the passphrase (`credential_vault.set_passphrase` / `credential_vault.change_passphrase`), unlocking (`credential_vault.unlock`), a failed unlock (`credential_vault.unlock_failed`), locking (`credential_vault.lock`) and resetting (`credential_vault.reset`) — never the passphrase
* [DNS profile](admin.md#dns-profiles) changes: `dns_profile.create`, `dns_profile.update`, `dns_profile.delete`, with the profile name and provider as detail — never its credentials
* Admin viewing the shared student portal login password
* Admin saving the [GitHub login](github.md) settings (`github_auth.update`, with whether it is enabled and for which organizations as detail — never the client secret)
* Admin saving the [GitLab login](gitlab.md) settings (`gitlab_auth.update`, with whether it is enabled, the GitLab host and the allowed groups as detail — never the secret)
* Automatic (system) actions: workspaces deleted for exceeding their configured lifetime, labs auto-destroyed past their scheduled deletion date

## Admin identity caveat

EasyLab's classic admin login is a single shared password (`LAB_ADMIN_PASSWORD`) with no concept of individual admin accounts — so an action taken by an admin who signed in this way is recorded with the generic actor **admin**, not a name. If [Azure AD admin login](azure-ad.md) is configured, the real signed-in email is used instead, since that flow verifies the admin's identity against your directory. Student actions (workspace creation and deletion) always show the student's real email, since student login always identifies an individual.
