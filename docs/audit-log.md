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

## What's tracked

* Lab actions: create, dry run, launch, destroy, retry (with or without an edited configuration), recreate, delete, template upload, lifecycle edit
* Workspace actions: student-initiated creation, admin-initiated deletion (single or bulk)
* Credential changes: saving OVH or Azure credentials (the credential values themselves are never recorded)
* Automatic (system) actions: workspaces deleted for exceeding their configured lifetime, labs auto-destroyed past their scheduled deletion date

## Admin identity caveat

EasyLab's classic admin login is a single shared password (`LAB_ADMIN_PASSWORD`) with no concept of individual admin accounts — so an action taken by an admin who signed in this way is recorded with the generic actor **admin**, not a name. If [Azure AD admin login](azure-ad.md) is configured, the real signed-in email is used instead, since that flow verifies the admin's identity against your directory. Student actions (workspace creation) always show the student's real email, since student login always identifies an individual.
