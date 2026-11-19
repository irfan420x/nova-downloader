package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func instantRunner(ctx context.Context, j *Job, report func(Progress)) error {
	report(Progress{Percent: 100, Done: 100, Total: 100})
	return nil
}

func TestAddAndComplete(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, 2, filepath.Join(dir, "jobs.json"), instantRunner)
	j := m.Add(&Job{URL: "https://example.com/f.zip", Title: "f.zip", Kind: KindFile})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Get(j.ID); got.Status == StatusDone {
			if got.Percent != 100 {
				t.Fatalf("percent = %v", got.Percent)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func TestPauseResume(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	runner := func(ctx context.Context, j *Job, report func(Progress)) error {
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
			return nil
		}
	}
	dir := t.TempDir()
	m := New(dir, 1, filepath.Join(dir, "jobs.json"), runner)
	j := m.Add(&Job{URL: "https://example.com/big.mp4", Title: "big.mp4", Kind: KindFile})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner never started")
	}
	if err := m.Pause(j.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := m.Get(j.ID); got.Status != StatusPaused {
		t.Fatalf("status after pause = %q", got.Status)
	}
	// Resume with a runner that finishes immediately.
	m.run = instantRunner
	if err := m.Resume(j.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Get(j.ID); got.Status == StatusDone {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("resumed job did not complete")
}

func TestFailAndRetry(t *testing.T) {
	boom := errors.New("boom")
	m := New(t.TempDir(), 1, "", func(ctx context.Context, j *Job, report func(Progress)) error {
		return boom
	})
	j := m.Add(&Job{URL: "https://example.com/x", Title: "x", Kind: KindFile})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Get(j.ID); got.Status == StatusFailed {
			if got.Error != "boom" {
				t.Fatalf("error = %q", got.Error)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.Get(j.ID); got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if err := m.Resume(j.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestPersistenceRestoresDownloadingAsPaused(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "jobs.json")
	m := New(dir, 1, state, instantRunner)
	j := m.Add(&Job{URL: "https://example.com/f.zip", Title: "f.zip", Kind: KindFile})
	// Wait for completion, then flip the persisted status to downloading
	// to simulate a crash mid-download.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Get(j.ID); got.Status == StatusDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(state)
	// Rewrite with status "downloading".
	raw := string(data)
	raw = replaceOnce(raw, `"status": "done"`, `"status": "downloading"`)
	if err := os.WriteFile(state, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	m2 := New(dir, 1, state, instantRunner)
	got := m2.Get(j.ID)
	if got == nil {
		t.Fatal("job not restored")
	}
	if got.Status != StatusPaused {
		t.Fatalf("restored status = %q, want paused", got.Status)
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestConcurrencyLimit(t *testing.T) {
	running := make(chan struct{}, 10)
	release := make(chan struct{})
	runner := func(ctx context.Context, j *Job, report func(Progress)) error {
		running <- struct{}{}
		<-release
		<-running
		return nil
	}
	m := New(t.TempDir(), 2, "", runner)
	for i := 0; i < 4; i++ {
		m.Add(&Job{URL: "https://example.com/x", Title: "x", Kind: KindFile})
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(running); n != 2 {
		t.Fatalf("concurrent jobs = %d, want 2", n)
	}
	close(release)
}

// Stream URLs and headers are in-memory only: they must NOT survive
// a save/load round-trip (they expire, and headers carry session
// cookies), while engine/platform must survive.
func TestStreamFieldsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "jobs.json")
	m := New(dir, 1, state, instantRunner)
	j := m.Add(&Job{
		URL: "https://www.tiktok.com/@x/video/1", Title: "vid", Kind: KindVideo,
		Engine: "api", Platform: "tiktok", FormatID: "tiktok-720p",
		StreamURL:     "https://v16-webapp-prime.us.tiktok.com/video.mp4",
		StreamHeaders: map[string]string{"Cookie": "ttwid=secret"},
		Alternates:    []string{"https://v19-webapp-prime.us.tiktok.com/video.mp4"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Get(j.ID); got.Status == StatusDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if stringContains(string(data), "ttwid=secret") || stringContains(string(data), "v16-webapp-prime") {
		t.Fatal("stream URL / cookie header persisted to jobs.json — must stay in memory only")
	}
	m2 := New(dir, 1, state, instantRunner)
	got := m2.Get(j.ID)
	if got == nil {
		t.Fatal("job not restored")
	}
	if got.Engine != "api" || got.Platform != "tiktok" || got.FormatID != "tiktok-720p" {
		t.Fatalf("engine/platform lost on reload: %+v", got)
	}
	if got.StreamURL != "" || got.StreamHeaders != nil || len(got.Alternates) != 0 {
		t.Fatalf("stream fields survived reload: %+v", got)
	}
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
