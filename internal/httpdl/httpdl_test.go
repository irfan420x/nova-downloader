package httpdl

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rangeServer serves content with real Range support, like a CDN would.
func rangeServer(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
			return
		}
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
		var start int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start >= int64(len(content)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)-int(start)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start:])
	}))
}

func TestDownloadFull(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	srv := rangeServer(t, content)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")
	if err := Download(context.Background(), srv.URL, dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part file left behind after success")
	}
}

func TestDownloadPauseResume(t *testing.T) {
	// 8 MiB served slowly so we can pause mid-flight.
	content := bytes.Repeat([]byte("abcdefgh"), 1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		start := int64(0)
		if rng := r.Header.Get("Range"); rng != "" {
			fmt.Sscanf(rng, "bytes=%d-", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.Header().Set("Content-Length", strconv.Itoa(len(content)-int(start)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
		}
		// Trickle: 64 KiB per 20ms.
		for off := start; off < int64(len(content)); {
			n := int64(64 * 1024)
			if off+n > int64(len(content)) {
				n = int64(len(content)) - off
			}
			_, _ = w.Write(content[off : off+n])
			fl.Flush()
			off += n
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "big.bin")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Download(ctx, srv.URL, dest, nil) }()
	time.Sleep(300 * time.Millisecond)
	cancel() // pause
	err := <-done
	if err == nil {
		t.Fatal("expected cancel error, got nil (download finished too fast to pause)")
	}
	part, err := os.Stat(dest + ".part")
	if err != nil {
		t.Fatalf("no .part after pause: %v", err)
	}
	if part.Size() == 0 || part.Size() >= int64(len(content)) {
		t.Fatalf("unexpected .part size %d", part.Size())
	}
	pausedAt := part.Size()

	// Resume: must finish and match the original bytes exactly.
	if err := Download(context.Background(), srv.URL, dest, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatalf("resumed content mismatch: got %d bytes, want %d (paused at %d)",
			len(got), len(content), pausedAt)
	}
}

func TestDownloadServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	dir := t.TempDir()
	err := Download(context.Background(), srv.URL, filepath.Join(dir, "x"), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}
