// Package httpdl downloads a direct file/image URL with HTTP Range
// resume. A partial file is kept as "<name>.part"; pause is a context
// cancel, resume re-issues a Range request from the partial size.
package httpdl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Progress is a live direct-download status update.
type Progress struct {
	Done  int64
	Total int64
	// SpeedBps is the current throughput in bytes per second.
	SpeedBps int64
}

const (
	bufSize      = 256 * 1024
	progressTick = 250 * time.Millisecond
)

// Download fetches rawURL into destPath (via destPath+".part").
// It resumes an existing .part file when the server supports ranges.
// On ctx cancel it returns ctx.Err() and keeps the .part file.
func Download(ctx context.Context, rawURL, destPath string, onProgress func(p Progress)) error {
	partPath := destPath + ".part"
	var done int64
	if fi, err := os.Stat(partPath); err == nil {
		done = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nova-downloader/1.0")
	resume := false
	if done > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", done))
		resume = true
	}
	client := &http.Client{Timeout: 0} // streaming; ctx governs the lifetime
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored Range (or fresh download): restart from zero.
		if resume {
			done = 0
			resume = false
		}
	case http.StatusPartialContent:
		if !resume {
			return fmt.Errorf("unexpected 206 response")
		}
	default:
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	total := resp.ContentLength
	if resume && resp.StatusCode == http.StatusPartialContent {
		total += done
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resume {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flag, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, bufSize)
	lastReport := time.Now()
	var lastDone = done
	var speed int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
		}
		if now := time.Now(); now.Sub(lastReport) >= progressTick {
			elapsed := now.Sub(lastReport).Seconds()
			if elapsed > 0 {
				speed = int64(float64(done-lastDone) / elapsed)
			}
			lastReport, lastDone = now, done
			if onProgress != nil {
				onProgress(Progress{Done: done, Total: total, SpeedBps: speed})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if onProgress != nil {
		onProgress(Progress{Done: done, Total: total, SpeedBps: speed})
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Avoid clobbering an existing finished file: add (1), (2), ...
	final := destPath
	for i := 1; ; i++ {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(destPath)
		base := destPath[:len(destPath)-len(ext)]
		final = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	if err := os.Rename(partPath, final); err != nil {
		return err
	}
	return nil
}
