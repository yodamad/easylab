---
icon: lucide/chart-spline
title: Stats
---
# Stats Dashboard

Navigate to **Stats** (accessible from the header or `/admin/stats`) to see aggregated deployment metrics.

## Project selector

Use the **Select a Project** dropdown to scope the view to a single lab stack name, or choose **All Projects** for a combined view.

## KPI cards

Four summary cards are shown at the top of the page:

![Stats KPI cards](screens/stats-kpi.png){width=700}

| Card | Description |
|------|-------------|
| **Workspaces Used** | Total number of workspaces ever provisioned across all tracked jobs |
| **Currently Active** | Number of completed (live) labs at the time of the last check |
| **Failed** | Number of labs that ended in a failed state |
| **Workspaces Auto-cleaned** | Cumulative count of workspaces deleted by the automatic cleanup service |

## Activity chart

A stacked bar chart shows the monthly breakdown of lab deployments (Succeeded / Failed / Destroyed) on the left axis, overlaid with a line showing the number of workspaces auto-cleaned that month on the right axis.

## Per-project breakdown

When **All Projects** is selected, a summary table is shown below the chart listing each project with its total, active, and failed lab counts.

![Per-project stats breakdown](screens/stats-projects.png){width=700}