package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// archivedMonthBucket mirrors the monthly counts GetProjectStats computes
// from live jobs, so archived (removed) jobs can be merged back into the
// same response shape without a separate code path in the handler.
type archivedMonthBucket struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Destroyed int `json:"destroyed"`
	Created   int `json:"created"`
	Cleaned   int `json:"cleaned"`
}

// archivedProjectTotal holds the per-project lab counts contributed by jobs
// that have been removed, mirroring projectSummary's Total/Failed fields.
type archivedProjectTotal struct {
	Total  int
	Failed int
}

// statsArchiveFileName is the file, inside dataDir, that preserves the
// monthly stats contribution of jobs after they are removed via RemoveJob.
const statsArchiveFileName = "stats-archive.json"

// StatsArchive preserves the monthly stats contribution of jobs after they
// are removed (e.g. via the admin "delete lab" action), so deleting an old
// destroyed/failed lab doesn't erase its history from the stats dashboard.
type StatsArchive struct {
	mu      sync.RWMutex
	buckets map[string]map[string]*archivedMonthBucket // stackName -> "2006-01" -> bucket
	path    string
}

// NewStatsArchive loads a previously persisted archive from dataDir, if any.
// A missing file is treated as an empty archive, matching JobManager's
// tolerance for a fresh data directory.
func NewStatsArchive(dataDir string) *StatsArchive {
	a := &StatsArchive{
		buckets: make(map[string]map[string]*archivedMonthBucket),
	}
	if dataDir != "" {
		a.path = filepath.Join(dataDir, statsArchiveFileName)
	}
	if a.path == "" {
		return a
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: failed to read stats archive %s: %v", a.path, err)
		}
		return a
	}
	if err := json.Unmarshal(data, &a.buckets); err != nil {
		log.Printf("Warning: failed to parse stats archive %s: %v", a.path, err)
		a.buckets = make(map[string]map[string]*archivedMonthBucket)
	}
	return a
}

// recordJob folds a job's monthly contribution into the archive and persists
// it. Called from JobManager.RemoveJob before the job's own record is
// deleted. Only terminal (destroyed/failed) jobs are ever removable via the
// admin UI, so anything else is ignored defensively.
func (a *StatsArchive) recordJob(job *Job) {
	if a == nil || job == nil {
		return
	}

	job.mu.RLock()
	status := job.Status
	stackName := ""
	if job.Config != nil {
		stackName = job.Config.StackName
	}
	createdAt := job.CreatedAt
	updatedAt := job.UpdatedAt
	events := append([]WorkspaceEvent(nil), job.WorkspaceEvents...)
	job.mu.RUnlock()

	if status != JobStatusFailed && status != JobStatusDestroyed {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	bucket := func(month string) *archivedMonthBucket {
		months, ok := a.buckets[stackName]
		if !ok {
			months = make(map[string]*archivedMonthBucket)
			a.buckets[stackName] = months
		}
		b, ok := months[month]
		if !ok {
			b = &archivedMonthBucket{}
			months[month] = b
		}
		return b
	}

	switch status {
	case JobStatusFailed:
		bucket(createdAt.Format("2006-01")).Failed++
	case JobStatusDestroyed:
		bucket(updatedAt.Format("2006-01")).Destroyed++
	}
	for _, evt := range events {
		month := evt.At.Format("2006-01")
		switch evt.Action {
		case WorkspaceEventCreated:
			bucket(month).Created++
		case WorkspaceEventDeleted:
			bucket(month).Cleaned++
		}
	}

	a.saveLocked()
}

// saveLocked persists the archive to disk. Caller must hold a.mu for writing.
func (a *StatsArchive) saveLocked() {
	if a.path == "" {
		return
	}
	data, err := json.MarshalIndent(a.buckets, "", "  ")
	if err != nil {
		log.Printf("Warning: failed to marshal stats archive: %v", err)
		return
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("Warning: failed to write stats archive: %v", err)
		return
	}
	if err := os.Rename(tmp, a.path); err != nil {
		log.Printf("Warning: failed to persist stats archive: %v", err)
	}
}

// forProject returns a snapshot of archived monthly buckets for a single
// project, or merged across all projects when project == "__all__".
func (a *StatsArchive) forProject(project string) map[string]archivedMonthBucket {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make(map[string]archivedMonthBucket)
	merge := func(months map[string]*archivedMonthBucket) {
		for month, b := range months {
			cur := out[month]
			cur.Succeeded += b.Succeeded
			cur.Failed += b.Failed
			cur.Destroyed += b.Destroyed
			cur.Created += b.Created
			cur.Cleaned += b.Cleaned
			out[month] = cur
		}
	}
	if project == "__all__" {
		for _, months := range a.buckets {
			merge(months)
		}
	} else if months, ok := a.buckets[project]; ok {
		merge(months)
	}
	return out
}

// projectTotals returns, per project, the archived Total (destroyed+failed)
// and Failed lab counts — used to keep the __all__ per-project summary table
// consistent after jobs contributing to it have been removed.
func (a *StatsArchive) projectTotals() map[string]archivedProjectTotal {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make(map[string]archivedProjectTotal)
	for project, months := range a.buckets {
		var t archivedProjectTotal
		for _, b := range months {
			t.Total += b.Destroyed + b.Failed
			t.Failed += b.Failed
		}
		out[project] = t
	}
	return out
}
