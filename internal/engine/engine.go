// Package engine resolves the yt-dlp helper binary NOVA Downloader relies on
// for video sites, and can fetch it on first run.
//
// Design decision (weighed, 2026-11-19): yt-dlp is NOT bundled in the repo
// or the Go binary. It is a ~15 MB Python zipapp that updates weekly; bundling
// it would bloat every release and ship a stale extractor within days.
// Instead the app looks for it in (1) the folder next to the app binary,
// (2) PATH, (3) the app data dir — and offers a one-click download from the
// official yt-dlp GitHub releases when it is missing. Direct file/image
// downloads never need yt-dlp at all.
package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Name of the yt-dlp binary for the current platform.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// ReleaseAsset is the file to fetch from the yt-dlp GitHub releases.
func releaseAsset() string {
	switch runtime.GOOS {
	case "windows":
		return "yt-dlp.exe"
	case "darwin":
		return "yt-dlp_macos"
	default:
		return "yt-dlp"
	}
}

// DataDir is where the app keeps its own data (downloaded engine, state).
func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "nova-downloader"), nil
}

// Find returns the path of a usable yt-dlp binary, or "" when none is found.
func Find() string {
	// 1. Next to the app binary (portable installs).
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), binaryName()); fileExists(p) {
			return p
		}
	}
	// 2. PATH.
	if p, err := exec.LookPath(binaryName()); err == nil {
		return p
	}
	// 3. App data dir (first-run download target).
	if dir, err := DataDir(); err == nil {
		if p := filepath.Join(dir, binaryName()); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Version runs `yt-dlp --version` and returns the trimmed output.
func Version(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// FFmpeg returns the path of ffmpeg when available (needed for mp3
// extraction and subtitle conversion), or "".
func FFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), "ffmpeg"); fileExists(p) {
			return p
		}
		if p := filepath.Join(filepath.Dir(exe), "ffmpeg.exe"); fileExists(p) {
			return p
		}
	}
	return ""
}

// Download fetches the official yt-dlp release binary into the app data dir.
// onProgress is called with bytes received and total (-1 when unknown).
func Download(ctx context.Context, onProgress func(received, total int64)) (string, error) {
	url := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/" + releaseAsset()
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, binaryName())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "nova-downloader/1.0")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("yt-dlp download: HTTP %s", resp.Status)
	}
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 128*1024)
	var received int64
	total := resp.ContentLength
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", werr
			}
			received += int64(n)
			if onProgress != nil {
				onProgress(received, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return "", err
	}
	return target, nil
}
