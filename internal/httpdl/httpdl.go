// Package httpdl downloads a direct file/image URL with HTTP Range
// resume. A partial file is kept as "<name>.part"; pause is a context
// cancel, resume re-issues a Range request from the partial size.
package httpdl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Progress is a live direct-download status update.
type Progress struct {
	Done  int64
	Total int64
	// SpeedBps is the current throughput in bytes per second.
	SpeedBps int64
}

// statusError is a non-2xx HTTP response received before any body
// bytes — a hard failure for this candidate (do not retry it).
type statusError struct{ status string }

func (e *statusError) Error() string { return "HTTP " + e.status }

// maxAttemptsPerCandidate bounds retries of one candidate URL on
// transient failures (mid-stream EOF / reset). A candidate that
// answers with a hard HTTP status is never retried. Six attempts:
// the VM's network truncates some googlevideo streams at random
// points (curl gets a different short size on every try of the same
// URL), and each attempt resumes from the .part file, so attempts
// accumulate progress instead of restarting.
const maxAttemptsPerCandidate = 6

const (
	bufSize      = 256 * 1024
	progressTick = 250 * time.Millisecond
)

// Download fetches rawURL into destPath (via destPath+".part").
// It resumes an existing .part file when the server supports ranges.
// On ctx cancel it returns ctx.Err() and keeps the .part file.
func Download(ctx context.Context, rawURL, destPath string, onProgress func(p Progress)) error {
	_, err := DownloadWithHeaders(ctx, rawURL, destPath, nil, nil, onProgress)
	return err
}

// DownloadWithHeaders is Download with per-request headers (e.g. the
// Cookie / Referer / User-Agent the NOVA API returns per quality —
// CDN gates need them) and alternate URLs for the same content
// (e.g. TikTok's UrlList hosts). Candidates are tried in order; a
// candidate that fails before or during the transfer falls through to
// the next one, resuming from the partial file. On ctx cancel it
// returns ctx.Err() and keeps the .part file. On success it returns
// the final file path (httpdl may add " (1)" on collisions).
func DownloadWithHeaders(ctx context.Context, rawURL, destPath string, headers map[string]string, alternates []string, onProgress func(p Progress)) (string, error) {
	candidates := make([]string, 0, len(alternates)+1)
	candidates = append(candidates, rawURL)
	candidates = append(candidates, alternates...)
	var lastErr error
	for _, cand := range candidates {
		var err error
		for attempt := 0; attempt < maxAttemptsPerCandidate; attempt++ {
			err = downloadOne(ctx, cand, headers, destPath, onProgress)
			if err == nil {
				return finalize(destPath)
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Hard HTTP status (403/404/…) — the candidate itself
			// refuses; retrying it is pointless, try the next one.
			var se *statusError
			if errors.As(err, &se) {
				break
			}
			// Transient mid-stream failure (EOF / reset): retry the
			// same candidate, resuming from the .part file.
			if attempt+1 < maxAttemptsPerCandidate {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(attempt+1) * time.Second):
				}
			}
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download URL worked")
	}
	return "", lastErr
}

// downloadOne streams one candidate URL into destPath+".part".
// It resumes an existing .part file when the server supports ranges.
// A nil error means the full body was written to the .part file
// (final rename is the caller's job).
func downloadOne(ctx context.Context, rawURL string, headers map[string]string, destPath string, onProgress func(p Progress)) error {
	partPath := destPath + ".part"
	var done int64
	if fi, err := os.Stat(partPath); err == nil {
		done = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	applyHeaders(req, headers)
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
		return &statusError{resp.Status}
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
	return f.Close()
}

// finalize renames destPath+".part" to the final name, avoiding
// clobbering an existing finished file: add (1), (2), ...
// It returns the final path.
func finalize(destPath string) (string, error) {
	partPath := destPath + ".part"
	final := destPath
	for i := 1; ; i++ {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(destPath)
		base := destPath[:len(destPath)-len(ext)]
		final = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	return final, os.Rename(partPath, final)
}

// applyHeaders sets the given headers on req. Range and Host are
// owned by the downloader and never taken from the map. A default
// User-Agent is set only when the map provides none.
func applyHeaders(req *http.Request, headers map[string]string) {
	ua := false
	for k, v := range headers {
		lk := strings.ToLower(k)
		if lk == "range" || lk == "host" {
			continue
		}
		if lk == "user-agent" {
			ua = true
		}
		req.Header.Set(k, v)
	}
	if !ua {
		req.Header.Set("User-Agent", "nova-downloader/1.0")
	}
}

// --- HLS -----------------------------------------------------------------

// DownloadHLS downloads an HLS (.m3u8) stream without ffmpeg: it
// fetches the playlist, picks the highest-bandwidth variant, downloads
// the media segments (reusing already-downloaded ones across pauses
// and restarts), and concatenates them into destPath. Segment files
// live in destPath+".hls-segs" until the download completes.
func DownloadHLS(ctx context.Context, playlistURL, destPath string, headers map[string]string, onProgress func(p Progress)) error {
	mediaURL, err := pickVariant(ctx, playlistURL, headers)
	if err != nil {
		return err
	}
	segments, initURL, err := fetchMediaPlaylist(ctx, mediaURL, headers)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("HLS playlist has no segments")
	}

	segDir := destPath + ".hls-segs"
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		return err
	}
	partPath := destPath + ".part"

	// Optional init segment (fMP4 streams): fetch once, prepend later.
	var initData []byte
	if initURL != "" {
		initData, err = fetchBytes(ctx, initURL, headers)
		if err != nil {
			return fmt.Errorf("HLS init segment: %w", err)
		}
	}

	var downloaded int64
	var completed int
	start := time.Now()
	lastReport := start
	for i, segURL := range segments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		segPath := filepath.Join(segDir, fmt.Sprintf("seg-%05d.ts", i))
		if fi, err := os.Stat(segPath); err == nil && fi.Size() > 0 {
			downloaded += fi.Size()
			completed++
			continue
		}
		n, err := fetchToFile(ctx, segURL, segPath, headers)
		if err != nil {
			return fmt.Errorf("HLS segment %d: %w", i, err)
		}
		downloaded += n
		completed++
		if onProgress != nil && time.Since(lastReport) >= progressTick {
			total := estimateTotal(downloaded, completed, len(segments))
			elapsed := time.Since(start).Seconds()
			var speed int64
			if elapsed > 0 {
				speed = int64(float64(downloaded) / elapsed)
			}
			onProgress(Progress{Done: downloaded, Total: total, SpeedBps: speed})
			lastReport = time.Now()
		}
	}

	// Concatenate: init segment (if any) + segments in order.
	out, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if len(initData) > 0 {
		if _, err := out.Write(initData); err != nil {
			out.Close()
			return err
		}
	}
	for i := range segments {
		segPath := filepath.Join(segDir, fmt.Sprintf("seg-%05d.ts", i))
		if err := appendFile(out, segPath); err != nil {
			out.Close()
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(Progress{Done: downloaded, Total: downloaded})
	}
	_ = os.RemoveAll(segDir)
	_, err = finalize(destPath)
	return err
}

// estimateTotal extrapolates the full size from the average segment
// size so far; it converges to the real total as segments complete.
func estimateTotal(downloaded int64, completed, total int) int64 {
	if completed <= 0 || total <= 0 {
		return 0
	}
	return downloaded * int64(total) / int64(completed)
}

func appendFile(dst *os.File, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(dst, f)
	return err
}

func fetchBytes(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, headers)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

func fetchToFile(ctx context.Context, rawURL, dest string, headers map[string]string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	applyHeaders(req, headers)
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return 0, err
	}
	return n, nil
}

// HLSSegmentExt returns the file extension of the first media
// segment of an HLS stream (".ts", ".m4s", …) — the container the
// concatenated download actually is. ".mp4" when it cannot be
// determined (extensionless segment URLs).
func HLSSegmentExt(ctx context.Context, playlistURL string, headers map[string]string) (string, error) {
	mediaURL, err := pickVariant(ctx, playlistURL, headers)
	if err != nil {
		return "", err
	}
	segments, _, err := fetchMediaPlaylist(ctx, mediaURL, headers)
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("HLS playlist has no segments")
	}
	ext := strings.ToLower(filepath.Ext(segments[0]))
	// Strip any query string the URL parser left on the extension.
	if i := strings.IndexByte(ext, '?'); i >= 0 {
		ext = ext[:i]
	}
	if ext == "" {
		return ".mp4", nil
	}
	return ext, nil
}
func pickVariant(ctx context.Context, playlistURL string, headers map[string]string) (string, error) {
	text, err := fetchText(ctx, playlistURL, headers)
	if err != nil {
		return "", err
	}
	bestURL, bestBW := "", int64(-1)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		bw := parseAttrInt(line, "BANDWIDTH")
		// The URI is the next non-empty, non-comment line.
		for j := i + 1; j < len(lines); j++ {
			u := strings.TrimSpace(lines[j])
			if u == "" || strings.HasPrefix(u, "#") {
				continue
			}
			if bw > bestBW {
				bestBW = bw
				bestURL = resolveURL(playlistURL, u)
			}
			break
		}
	}
	if bestURL != "" {
		return bestURL, nil
	}
	if !strings.Contains(text, "#EXTM3U") {
		return "", fmt.Errorf("URL did not return an m3u8 playlist")
	}
	return playlistURL, nil
}

// fetchMediaPlaylist returns the segment URLs (resolved absolute) and
// the EXT-X-MAP init-segment URL ("" when none) of a media playlist.
func fetchMediaPlaylist(ctx context.Context, mediaURL string, headers map[string]string) (segments []string, initURL string, err error) {
	text, err := fetchText(ctx, mediaURL, headers)
	if err != nil {
		return nil, "", err
	}
	if !strings.Contains(text, "#EXTM3U") {
		return nil, "", fmt.Errorf("URL did not return an m3u8 playlist")
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			if uri := parseAttrString(line, "URI"); uri != "" {
				initURL = resolveURL(mediaURL, uri)
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		segments = append(segments, resolveURL(mediaURL, line))
	}
	return segments, initURL, nil
}

func fetchText(ctx context.Context, rawURL string, headers map[string]string) (string, error) {
	b, err := fetchBytes(ctx, rawURL, headers)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseAttrInt(line, key string) int64 {
	v := parseAttrString(line, key)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func parseAttrString(line, key string) string {
	// Attributes live after the first ':'; split on commas not in quotes.
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return ""
	}
	rest := line[colon+1:]
	for _, part := range splitAttrs(rest) {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == key {
			return strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
	}
	return ""
}

func splitAttrs(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	for _, c := range s {
		switch {
		case c == '"':
			inQuotes = !inQuotes
			cur.WriteRune(c)
		case c == ',' && !inQuotes:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
