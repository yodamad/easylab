package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewStatsArchive_MissingFile(t *testing.T) {
	dir := t.TempDir()
	a := NewStatsArchive(dir)

	require.Empty(t, a.forProject("__all__"))
	require.Empty(t, a.projectTotals())
}

func TestStatsArchive_RecordJob_StatusGating(t *testing.T) {
	tests := []struct {
		name       string
		status     JobStatus
		wantRecord bool
	}{
		{"pending not archived", JobStatusPending, false},
		{"running not archived", JobStatusRunning, false},
		{"completed not archived", JobStatusCompleted, false},
		{"dry-run not archived", JobStatusDryRunCompleted, false},
		{"failed archived", JobStatusFailed, true},
		{"destroyed archived", JobStatusDestroyed, true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewStatsArchive("")
			job := &Job{
				Status:    tt.status,
				CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Config:    &LabConfig{StackName: "proj"},
			}
			a.recordJob(job)

			months := a.forProject("proj")
			if tt.wantRecord {
				require.NotEmpty(t, months, "expected job to be archived")
			} else {
				require.Empty(t, months, "expected job not to be archived")
			}
		})
	}
}

func TestStatsArchive_RecordJob_MonthAttribution(t *testing.T) {
	a := NewStatsArchive("")

	failedJob := &Job{
		Status:    JobStatusFailed,
		CreatedAt: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), // must be ignored for Failed
		Config:    &LabConfig{StackName: "proj"},
	}
	a.recordJob(failedJob)

	destroyedJob := &Job{
		Status:    JobStatusDestroyed,
		CreatedAt: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), // must be ignored for Destroyed
		UpdatedAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj"},
	}
	a.recordJob(destroyedJob)

	months := a.forProject("proj")
	require.Equal(t, 1, months["2026-01"].Failed, "failed job should bucket by CreatedAt month")
	require.Equal(t, 0, months["2026-01"].Destroyed)
	require.Equal(t, 1, months["2026-03"].Destroyed, "destroyed job should bucket by UpdatedAt month")
}

func TestStatsArchive_RecordJob_WorkspaceEvents(t *testing.T) {
	a := NewStatsArchive("")
	job := &Job{
		Status:    JobStatusDestroyed,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj"},
		WorkspaceEvents: []WorkspaceEvent{
			{Action: WorkspaceEventCreated, At: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)},
			{Action: WorkspaceEventCreated, At: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)},
			{Action: WorkspaceEventDeleted, At: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)},
		},
	}
	a.recordJob(job)

	months := a.forProject("proj")
	require.Equal(t, 2, months["2026-01"].Created)
	require.Equal(t, 1, months["2026-01"].Cleaned)
}

func TestStatsArchive_ForProject_AllAggregatesAcrossProjects(t *testing.T) {
	a := NewStatsArchive("")
	a.recordJob(&Job{
		Status:    JobStatusFailed,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj-a"},
	})
	a.recordJob(&Job{
		Status:    JobStatusDestroyed,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj-b"},
	})

	all := a.forProject("__all__")
	require.Equal(t, 1, all["2026-01"].Failed)
	require.Equal(t, 1, all["2026-01"].Destroyed)

	onlyA := a.forProject("proj-a")
	require.Equal(t, 1, onlyA["2026-01"].Failed)
	require.Equal(t, 0, onlyA["2026-01"].Destroyed)
}

func TestStatsArchive_ProjectTotals(t *testing.T) {
	a := NewStatsArchive("")
	a.recordJob(&Job{
		Status:    JobStatusFailed,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj-a"},
	})
	a.recordJob(&Job{
		Status:    JobStatusDestroyed,
		CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj-a"},
	})

	totals := a.projectTotals()
	got, ok := totals["proj-a"]
	require.True(t, ok)
	require.Equal(t, 2, got.Total)
	require.Equal(t, 1, got.Failed)
}

func TestStatsArchive_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	a := NewStatsArchive(dir)
	a.recordJob(&Job{
		Status:    JobStatusDestroyed,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Config:    &LabConfig{StackName: "proj-a"},
	})

	reloaded := NewStatsArchive(dir)
	months := reloaded.forProject("proj-a")
	require.Equal(t, 1, months["2026-03"].Destroyed)
}
