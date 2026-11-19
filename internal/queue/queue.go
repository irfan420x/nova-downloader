// Package queue is the download job manager: a bounded-concurrency queue
// with pause / resume / cancel / retry, progress reporting, and
// persistence of unfinished jobs across restarts.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status of a job.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusPaused      Status = "paused"
	StatusDone        Status = "done"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
)

// Kind of a job.
type Kind string

const (
	KindVideo Kind = "video" // via yt-dlp
	KindFile  Kind = "file"  // direct HTTP download
)

// Progress is a live status update from a runner.
type Progress struct {
	Percent  float64
	Done     int64
	Total    int64
	Speed    string // human-readable, e.g. "1.2 MiB/s"
	ETA      string
	Filename string
}

// Job is one download.
type Job struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	Kind       Kind      `json:"kind"`
	FormatID   string    `json:"format_id"`   // video only; "" = best
	FormatNote string    `json:"format_note"` // e.g. "1080p · mp4"
	AudioOnly  bool      `json:"audio_only"`
	Subtitles  bool      `json:"subtitles"`
	SubLangs   string    `json:"sub_langs"`
	Filename   string    `json:"filename"` // set once known
	OutDir     string    `json:"out_dir"`
	Status     Status    `json:"status"`
	Percent    float64   `json:"percent"`
	DoneBytes  int64     `json:"done_bytes"`
	TotalBytes int64     `json:"total_bytes"`
	Speed      string    `json:"speed"`
	ETA        string    `json:"eta"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
	// Engine is how the job was resolved: "api" (NOVA API), "ytdlp"
	// (fallback), "file" (direct download). "" on old saved jobs
	// means the legacy yt-dlp video path.
	Engine   string `json:"engine"`
	Platform string `json:"platform"`
	// Resolved stream data — in-memory ONLY, never persisted:
	// stream URLs expire within minutes/hours, and headers carry
	// short-lived session cookies. A job resumed after a restart
	// re-resolves its page URL fresh instead of trusting stale data.
	StreamURL          string            `json:"-"`
	StreamHeaders      map[string]string `json:"-"`
	StreamExt          string            `json:"-"`
	Alternates         []string          `json:"-"`
	AudioStreamURL     string            `json:"-"`
	AudioStreamHeaders map[string]string `json:"-"`
	AudioStreamExt     string            `json:"-"`
	// Merged is set when the chosen quality is video-only and the
	// runner should also fetch the audio stream and merge with ffmpeg.
	Merged bool `json:"merged"`

	cancel context.CancelFunc `json:"-"`
	done   chan struct{}      `json:"-"`
}

// ErrPaused is returned by a Runner when a job was paused (context
// cancelled). Runners wrap it together with context.Canceled so the
// queue can recognise either sentinel.
var ErrPaused = errors.New("download paused")

// Runner downloads one job. It must honour ctx cancellation (pause)
// and report progress through the callback. Returning ctx.Err() (or
// yt.ErrPaused) means "paused, resumable".
type Runner func(ctx context.Context, j *Job, report func(p Progress)) error

// Manager owns the job list and runs up to MaxConcurrent jobs at once.
type Manager struct {
	mu            sync.Mutex
	jobs          []*Job
	byID          map[string]*Job
	active        int
	maxConcurrent int
	outDir        string
	run           Runner
	statePath     string
	seq           int
}

// New creates a Manager. statePath is the JSON file for persistence
// ("" disables persistence). Jobs found there in "downloading" state
// are restored as "paused".
func New(outDir string, maxConcurrent int, statePath string, run Runner) *Manager {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	m := &Manager{
		byID:          map[string]*Job{},
		maxConcurrent: maxConcurrent,
		outDir:        outDir,
		run:           run,
		statePath:     statePath,
	}
	m.load()
	return m
}

// SetRunner installs (or replaces) the download runner. Jobs already
// running keep their old runner; queued jobs use the new one.
func (m *Manager) SetRunner(run Runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run = run
}

// OutDir returns the current download directory.
func (m *Manager) OutDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outDir
}

// SetOutDir changes the download directory for future jobs.
func (m *Manager) SetOutDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outDir = dir
}

// MaxConcurrent returns the concurrency limit.
func (m *Manager) MaxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxConcurrent
}

// SetMaxConcurrent changes the concurrency limit.
func (m *Manager) SetMaxConcurrent(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n >= 1 {
		m.maxConcurrent = n
	}
	m.maybeStartLocked()
}

// Add enqueues a job and returns it.
func (m *Manager) Add(j *Job) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	j.ID = fmt.Sprintf("job-%d-%d", time.Now().Unix(), m.seq)
	j.Status = StatusQueued
	j.CreatedAt = time.Now()
	if j.OutDir == "" {
		j.OutDir = m.outDir
	}
	j.done = make(chan struct{})
	m.jobs = append(m.jobs, j)
	m.byID[j.ID] = j
	m.saveLocked()
	m.maybeStartLocked()
	return j
}

// List returns a snapshot of all jobs, oldest first.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, len(m.jobs))
	for i, j := range m.jobs {
		c := *j
		out[i] = &c
	}
	return out
}

// Get returns a snapshot of one job, or nil.
func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.byID[id]; ok {
		c := *j
		return &c
	}
	return nil
}

// Pause cancels a downloading/queued job (queued jobs just go paused).
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.byID[id]
	if !ok {
		return errors.New("job not found")
	}
	switch j.Status {
	case StatusDownloading:
		j.Status = StatusPaused
		if j.cancel != nil {
			j.cancel()
		}
	case StatusQueued:
		j.Status = StatusPaused
	default:
		return fmt.Errorf("cannot pause a %s job", j.Status)
	}
	m.saveLocked()
	m.maybeStartLocked()
	return nil
}

// Resume re-queues a paused/failed job.
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.byID[id]
	if !ok {
		return errors.New("job not found")
	}
	switch j.Status {
	case StatusPaused, StatusFailed, StatusCancelled:
		j.Status = StatusQueued
		j.Error = ""
		j.done = make(chan struct{})
	default:
		return fmt.Errorf("cannot resume a %s job", j.Status)
	}
	m.saveLocked()
	m.maybeStartLocked()
	return nil
}

// Cancel stops a job and marks it cancelled.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.byID[id]
	if !ok {
		return errors.New("job not found")
	}
	switch j.Status {
	case StatusDownloading:
		j.Status = StatusCancelled
		if j.cancel != nil {
			j.cancel()
		}
	case StatusQueued, StatusPaused, StatusFailed:
		j.Status = StatusCancelled
	default:
		return fmt.Errorf("cannot cancel a %s job", j.Status)
	}
	m.saveLocked()
	m.maybeStartLocked()
	return nil
}

// ClearFinished removes done/cancelled/failed jobs from the list.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keep []*Job
	for _, j := range m.jobs {
		switch j.Status {
		case StatusDone, StatusCancelled, StatusFailed:
			delete(m.byID, j.ID)
		default:
			keep = append(keep, j)
		}
	}
	m.jobs = keep
	m.saveLocked()
}

// maybeStartLocked starts queued jobs while a slot is free.
// Caller must hold m.mu.
func (m *Manager) maybeStartLocked() {
	for m.active < m.maxConcurrent {
		var next *Job
		for _, j := range m.jobs {
			if j.Status == StatusQueued {
				next = j
				break
			}
		}
		if next == nil {
			return
		}
		next.Status = StatusDownloading
		next.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		next.cancel = cancel
		m.active++
		go m.execute(ctx, next)
	}
	m.saveLocked()
}

func (m *Manager) execute(ctx context.Context, j *Job) {
	if m.run == nil {
		m.mu.Lock()
		j.Status = StatusFailed
		j.Error = "no download runner configured"
		m.active--
		close(j.done)
		m.saveLocked()
		m.maybeStartLocked()
		m.mu.Unlock()
		return
	}
	err := m.run(ctx, j, func(p Progress) {
		m.mu.Lock()
		j.Percent = p.Percent
		j.DoneBytes = p.Done
		j.TotalBytes = p.Total
		j.Speed = p.Speed
		j.ETA = p.ETA
		if p.Filename != "" {
			j.Filename = filepath.Base(p.Filename)
		}
		m.mu.Unlock()
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, ErrPaused) {
			// Paused: status was already set by Pause().
			if j.Status == StatusDownloading {
				j.Status = StatusPaused
			}
		} else {
			j.Status = StatusFailed
			j.Error = err.Error()
		}
	} else {
		j.Status = StatusDone
		j.Percent = 100
	}
	close(j.done)
	m.saveLocked()
	m.maybeStartLocked()
}

// saveLocked writes the job list to disk. Caller must hold m.mu.
func (m *Manager) saveLocked() {
	if m.statePath == "" {
		return
	}
	data, err := json.MarshalIndent(m.jobs, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(m.statePath)
	_ = os.MkdirAll(dir, 0o755)
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, m.statePath)
}

// load restores jobs from disk; downloading jobs become paused.
func (m *Manager) load() {
	if m.statePath == "" {
		return
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return
	}
	for _, j := range jobs {
		if j.Status == StatusDownloading {
			j.Status = StatusPaused
		}
		j.done = make(chan struct{})
		m.jobs = append(m.jobs, j)
		m.byID[j.ID] = j
		m.seq++
	}
}
