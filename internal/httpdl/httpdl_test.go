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

func TestDownloadWithHeadersAlternates(t *testing.T) {
	// First host 403s (like TikTok's v16 without the right headers);
	// the alternate host serves the bytes. The Cookie + Referer the
	// API returned must arrive at the working host.
	content := []byte("hello-cdn-bytes")
	var seenCookie, seenReferer, seenUA string
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		seenReferer = r.Header.Get("Referer")
		seenUA = r.Header.Get("User-Agent")
		w.Header().Set("Accept-Ranges", "bytes")
		_, _ = w.Write(content)
	}))
	defer good.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "video.mp4")
	headers := map[string]string{
		"Cookie":     "ttwid=abc; tt_csrf_token=def",
		"Referer":    "https://www.tiktok.com/",
		"User-Agent": "Mozilla/5.0 (Linux; Android 16)",
	}
	if _, err := DownloadWithHeaders(context.Background(), bad.URL, dest, headers, []string{good.URL}, nil); err != nil {
		t.Fatalf("download with alternates: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: %q", got)
	}
	if seenCookie != "ttwid=abc; tt_csrf_token=def" {
		t.Fatalf("Cookie not sent to CDN: %q", seenCookie)
	}
	if seenReferer != "https://www.tiktok.com/" {
		t.Fatalf("Referer not sent to CDN: %q", seenReferer)
	}
	if seenUA != "Mozilla/5.0 (Linux; Android 16)" {
		t.Fatalf("API User-Agent not used: %q", seenUA)
	}
}

func TestDownloadWithHeadersAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bad.Close()
	dir := t.TempDir()
	_, err := DownloadWithHeaders(context.Background(), bad.URL, filepath.Join(dir, "x"), nil, []string{bad.URL}, nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

// A server that kills the connection mid-stream on the first attempt
// (like the YouTube "unexpected EOF" seen live) must be retried from
// the partial file — the download completes instead of failing.
func TestDownloadWithHeadersRetriesTransientEOF(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 1024) // 10 KiB
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Accept-Ranges", "bytes")
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
		if calls == 1 {
			// First attempt: half the bytes, then die (truncated body
			// vs Content-Length => "unexpected EOF" on the client).
			_, _ = w.Write(content[:len(content)/2])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("no hijacker")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write(content[start:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "file.bin")
	if _, err := DownloadWithHeaders(context.Background(), srv.URL, dest, nil, nil, nil); err != nil {
		t.Fatalf("download with transient EOF: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after retry: got %d bytes, want %d", len(got), len(content))
	}
	if calls < 2 {
		t.Fatalf("expected a retry, calls = %d", calls)
	}
}

func TestDownloadHLS(t *testing.T) {
	// Master playlist with two variants; the downloader must pick the
	// higher-bandwidth one, fetch its segments, and concat them.
	segA, segB, segC := []byte("SEGMENT-A-"), []byte("SEGMENT-B-"), []byte("SEGMENT-C")
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
/low.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
/high.m3u8
`))
	})
	mux.HandleFunc("/low.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nlow0.ts\n"))
	})
	mux.HandleFunc("/high.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nseg0.ts\n#EXTINF:10,\nseg1.ts\n#EXTINF:10,\nseg2.ts\n"))
	})
	mux.HandleFunc("/seg0.ts", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(segA) })
	mux.HandleFunc("/seg1.ts", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(segB) })
	mux.HandleFunc("/seg2.ts", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(segC) })
	mux.HandleFunc("/low0.ts", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("LOW")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "stream.mp4")
	if err := DownloadHLS(context.Background(), srv.URL+"/master.m3u8", dest, nil, nil); err != nil {
		t.Fatalf("HLS download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	want := append(append(append([]byte{}, segA...), segB...), segC...)
	if !bytes.Equal(got, want) {
		t.Fatalf("HLS content = %q, want %q (must be the HIGH variant)", got, want)
	}
	if _, err := os.Stat(dest + ".hls-segs"); !os.IsNotExist(err) {
		t.Fatal("segment temp dir left behind after success")
	}
}

func TestHLSSegmentExt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
/low.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
/high.m3u8
`))
	})
	mux.HandleFunc("/high.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nseg0.ts\n#EXTINF:10,\nseg1.ts\n"))
	})
	mux.HandleFunc("/low.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nseg0.ts\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ext, err := HLSSegmentExt(context.Background(), srv.URL+"/master.m3u8", nil)
	if err != nil {
		t.Fatalf("HLSSegmentExt: %v", err)
	}
	if ext != ".ts" {
		t.Fatalf("segment ext = %q, want .ts (highest-bandwidth variant)", ext)
	}
}

func TestDownloadHLSMediaPlaylist(t *testing.T) {
	// A media playlist (no variants) downloads as a single stream.
	mux := http.NewServeMux()
	mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nonly.ts\n"))
	})
	mux.HandleFunc("/only.ts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ONLY-SEGMENT"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "stream.mp4")
	if err := DownloadHLS(context.Background(), srv.URL+"/media.m3u8", dest, nil, nil); err != nil {
		t.Fatalf("HLS media download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "ONLY-SEGMENT" {
		t.Fatalf("HLS content = %q", got)
	}
}
