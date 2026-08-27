---
icon: lucide/chart-spline
title: Stats
---
# Stats Dashboard

Navigate to **Stats** (accessible from the header or `/admin/stats`) to see aggregated deployment metrics.

Figures are cached for up to 30 seconds per project view, so a just-completed change (a lab finishing, a workspace being created or cleaned) may take up to 30 seconds to appear here.

## Project selector

Use the **Select a Project** dropdown to scope the view to a single lab stack name, or choose **All Projects** for a combined view. The two views show different figures:

- **A single project** — a lab stack has exactly one outcome (it succeeded, failed, or was destroyed), so lab-level counts aren't useful there. The page shows **workspace-only** figures for that lab instead.
- **All Projects** — figures combine both lab-level and workspace-level counts across every tracked lab.

## KPI cards

Four summary cards are shown at the top of the page:

![Stats KPI cards](screens/stats-kpi.png){width=700}

| Card | All Projects | Single project |
|------|--------------|----------------|
| **Workspaces Used** | Total workspaces ever created across all tracked jobs, whether or not a lab has an auto-expiry lifetime configured | Same, scoped to this lab |
| **Currently Active** | Number of completed (live) labs at the time of the last check | Renamed **Active Workspaces**: workspaces created minus workspaces cleaned for this lab |
| **Failed** | Number of labs that ended in a failed state | Hidden (not meaningful for a single lab) |
| **Workspaces Cleaned** | Cumulative count of workspaces deleted, whether automatically by the cleanup service or manually by an admin/student | Same, scoped to this lab |

Figures are preserved even after an old destroyed or failed lab is removed
from the admin list — deleting a lab drops it from the labs list, but its
historical contribution to these KPIs and to the activity chart below
remains.

## Activity chart

- **All Projects**: four lines — **Labs** (labs succeeded, failed, or destroyed that month, left axis) plus three workspace lines on the right axis: **Workspaces Total** (created that month), **Workspaces Active** (a running total: workspaces created so far minus workspaces cleaned so far, i.e. how many were alive as of that month), and **Workspaces Cleaned** (cleaned that month). A destroyed lab is counted in the month it was destroyed, not the month it was created.
- **Single project**: two lines, both on the same axis — **Workspaces created** and **Workspaces cleaned** for that lab.

## Per-project breakdown

When **All Projects** is selected, a summary table is shown below the chart listing each project with its total, active, and failed lab counts.

![Per-project stats breakdown](screens/stats-projects.png){width=700}