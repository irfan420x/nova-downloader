// Package yt wraps the yt-dlp helper binary: probing a link for its
// metadata and formats, and downloading a chosen format.
//
// All parsing is done on yt-dlp's stable machine-readable outputs
// (`-J` JSON for probing, `--newline` progress lines for downloading),
// never on screen-scraped HTML.
package yt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Format is one downloadable stream variant reported by `yt-dlp -J`.
type Format struct {
	ID             string   `json:"format_id"`
	Ext            string   `json:"ext"`
	Resolution     string   `json:"resolution"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	FPS            float64  `json:"fps"`
	Filesize       *int64   `json:"filesize"`
	FilesizeApprox *int64   `json:"filesize_approx"`
	VCodec         string   `json:"vcodec"`
	ACodec         string   `json:"acodec"`
	TBR            *float64 `json:"tbr"`
	FormatNote     string   `json:"format_note"`
	Protocol       string   `json:"protocol"`
	// Stream data — set only for NOVA API resolutions: the direct
	// stream URL, the headers the CDN requires (Cookie / Referer /
	// User-Agent), and alternate hosts for the same content.
	StreamURL     string            `json:"stream_url,omitempty"`
	StreamHeaders map[string]string `json:"stream_headers,omitempty"`
	Alternates    []string          `json:"alternates,omitempty"`
}

// Size returns the best-known byte size, or 0 when unknown.
func (f Format) Size() int64 {
	if f.Filesize != nil {
		return *f.Filesize
	}
	if f.FilesizeApprox != nil {
		return *f.FilesizeApprox
	}
	return 0
}

// HasVideo reports whether the format carries a video stream.
func (f Format) HasVideo() bool { return f.VCodec != "" && f.VCodec != "none" }

// HasAudio reports whether the format carries an audio stream.
func (f Format) HasAudio() bool { return f.ACodec != "" && f.ACodec != "none" }

// Combined reports whether the format is a single file with both
// video and audio (what most users want to pick).
func (f Format) Combined() bool { return f.HasVideo() && f.HasAudio() }

// Label is a short human-readable description, e.g. "1080p · mp4 · 84.2 MB".
func (f Format) Label() string {
	parts := []string{}
	if f.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", f.Height))
		if f.FPS > 0 {
			parts[len(parts)-1] = fmt.Sprintf("%dp%d", f.Height, int(f.FPS))
		}
	} else if f.TBR != nil {
		parts = append(parts, fmt.Sprintf("%.0f kbps", *f.TBR))
	}
	if f.Ext != "" {
		parts = append(parts, f.Ext)
	}
	if s := f.Size(); s > 0 {
		parts = append(parts, HumanSize(s))
	}
	if f.FormatNote != "" {
		parts = append(parts, f.FormatNote)
	}
	return strings.Join(parts, " · ")
}

// PlaylistEntry is one item of a playlist probe.
type PlaylistEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Info is the result of probing a URL with `yt-dlp -J --flat-playlist`.
type Info struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Uploader   string          `json:"uploader"`
	WebpageURL string          `json:"webpage_url"`
	Thumbnail  string          `json:"thumbnail"`
	Duration   *float64        `json:"duration"`
	Type       string          `json:"_type"` // "video" or "playlist"
	Extractor  string          `json:"extractor"`
	Formats    []Format        `json:"formats"`
	Entries    []PlaylistEntry `json:"entries"`
}

// IsPlaylist reports whether the probed URL is a playlist/channel list.
func (in *Info) IsPlaylist() bool { return in.Type == "playlist" }

// CombinedFormats returns video+audio formats, best quality first.
// HLS (m3u8) variants are skipped — the https variants are the same
// streams in a directly downloadable form.
func (in *Info) CombinedFormats() []Format {
	var out []Format
	for _, f := range in.Formats {
		if f.Combined() && !strings.HasPrefix(f.Protocol, "m3u8") {
			out = append(out, f)
		}
	}
	sortFormatsByQuality(out)
	return out
}

// MergedOptions synthesises "video + audio" choices for sites (like
// modern YouTube) that no longer ship single-file combined formats.
// Each option is a yt-dlp format selector such as
// "bv*[height<=1080]+ba/b" — yt-dlp downloads the best video at that
// height plus the best audio and merges them. One option per height,
// best first, with a size estimate (video + audio).
func (in *Info) MergedOptions() []Format {
	audio := in.AudioOnlyFormats()
	var audioSize int64
	if len(audio) > 0 {
		audioSize = audio[0].Size()
	}
	seen := map[int]bool{}
	var out []Format
	for _, f := range in.VideoOnlyFormats() {
		if f.Height <= 0 || seen[f.Height] {
			continue
		}
		seen[f.Height] = true
		size := f.Size() + audioSize
		sz := size
		out = append(out, Format{
			ID:             fmt.Sprintf("bv*[height<=%d]+ba/b", f.Height),
			Ext:            "mp4",
			Width:          f.Width,
			Height:         f.Height,
			FPS:            f.FPS,
			FilesizeApprox: &sz,
			VCodec:         "merged",
			ACodec:         "merged",
			FormatNote:     "merged",
		})
	}
	return out
}

// VideoOnlyFormats returns video-only streams, best quality first.
// HLS (m3u8) variants are skipped — the https variants are the same
// streams in a directly downloadable form.
func (in *Info) VideoOnlyFormats() []Format {
	var out []Format
	for _, f := range in.Formats {
		if f.HasVideo() && !f.HasAudio() && !strings.HasPrefix(f.Protocol, "m3u8") {
			out = append(out, f)
		}
	}
	sortFormatsByQuality(out)
	return out
}

// AudioOnlyFormats returns audio-only streams, best bitrate first.
// "-drc" (dynamic range compression) duplicates are skipped.
func (in *Info) AudioOnlyFormats() []Format {
	var out []Format
	for _, f := range in.Formats {
		if f.HasAudio() && !f.HasVideo() && !strings.HasSuffix(f.ID, "-drc") {
			out = append(out, f)
		}
	}
	// Best bitrate first.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if bitrate(out[j]) > bitrate(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func bitrate(f Format) float64 {
	if f.TBR != nil {
		return *f.TBR
	}
	return 0
}

func sortFormatsByQuality(fs []Format) {
	for i := 0; i < len(fs); i++ {
		for j := i + 1; j < len(fs); j++ {
			if fs[j].Height > fs[i].Height ||
				(fs[j].Height == fs[i].Height && bitrate(fs[j]) > bitrate(fs[i])) {
				fs[i], fs[j] = fs[j], fs[i]
			}
		}
	}
}

// BestFormat returns the highest-quality combined format, or "" when none.
func (in *Info) BestFormat() string {
	if c := in.CombinedFormats(); len(c) > 0 {
		return c[0].ID
	}
	return ""
}

// Probe runs `yt-dlp -J --flat-playlist` and parses the JSON.
// A playlist URL returns Type "playlist" with Entries (capped by yt-dlp).
// cookiesFile ("" = none) is passed as --cookies when set.
func Probe(ctx context.Context, ytPath, rawURL, cookiesFile string) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"-J", "--flat-playlist", "--no-warnings"}
	if cookiesFile != "" {
		args = append(args, "--cookies", cookiesFile)
	}
	args = append(args, rawURL)
	cmd := exec.CommandContext(ctx, ytPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp probe failed: %s", firstLine(msg))
	}
	var info Info
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := dec.Decode(&info); err != nil {
		return nil, fmt.Errorf("yt-dlp probe: bad JSON: %w", err)
	}
	if info.Title == "" && !info.IsPlaylist() {
		return nil, errors.New("yt-dlp probe: no title returned")
	}
	return &info, nil
}

// Progress is a live download status update.
type Progress struct {
	Percent    float64
	Downloaded int64
	Total      int64
	Speed      string
	ETA        string
	Filename   string
}

// ErrPaused is returned by Download when the context was cancelled
// (pause). The partial file is kept so a later resume continues it.
var ErrPaused = errors.New("download paused")

// Options controls one yt-dlp download.
type Options struct {
	URL         string
	FormatID    string // "" = best video+audio
	OutDir      string
	AudioOnly   bool // extract mp3 (needs ffmpeg)
	Subtitles   bool
	SubLangs    string // e.g. "en,bn" — "" means "en"
	CookiesFile string // optional cookies.txt (for bot-checked / age-gated videos)
	// OnProgress is called for each parsed progress line.
	OnProgress func(p Progress)
	// OnFilename is called once the destination filename is known.
	OnFilename func(name string)
}

// Download runs yt-dlp for a single video. It blocks until the download
// finishes, fails, or ctx is cancelled (pause).
func Download(ctx context.Context, ytPath string, o Options) error {
	format := o.FormatID
	if format == "" {
		format = "bv*+ba/b"
	}
	args := []string{
		"--newline", "--no-colors", "--no-warnings",
		"--no-playlist",
		"--continue",
		"--merge-output-format", "mp4",
		"-f", format,
		"-o", o.OutDir + "/%(title)s.%(ext)s",
	}
	if o.AudioOnly {
		args = append(args, "--extract-audio", "--audio-format", "mp3", "--audio-quality", "0")
	}
	if o.Subtitles {
		langs := o.SubLangs
		if langs == "" {
			langs = "en"
		}
		args = append(args, "--write-subs", "--sub-langs", langs, "--sub-format", "srt/best")
	}
	if o.CookiesFile != "" {
		args = append(args, "--cookies", o.CookiesFile)
	}
	args = append(args, o.URL)

	cmd := exec.CommandContext(ctx, ytPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Drain stderr in the background, keeping the last few lines so a
	// failure can report yt-dlp's real error (bot check, geo-block, ...).
	var stderrMu sync.Mutex
	var stderrTail []string
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			stderrMu.Lock()
			stderrTail = append(stderrTail, line)
			if len(stderrTail) > 5 {
				stderrTail = stderrTail[len(stderrTail)-5:]
			}
			stderrMu.Unlock()
		}
	}()
	scanErr := scanProgress(stdout, o)
	<-stderrDone
	waitErr := cmd.Wait()
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			// Paused: wrap both sentinels so callers can test either.
			return fmt.Errorf("%w: %w", ErrPaused, ctx.Err())
		}
		stderrMu.Lock()
		tail := append([]string(nil), stderrTail...)
		stderrMu.Unlock()
		if msg := lastErrorLine(tail); msg != "" {
			return fmt.Errorf("yt-dlp: %s", msg)
		}
		return fmt.Errorf("yt-dlp exited: %w", waitErr)
	}
	return nil
}

// lastErrorLine picks the most useful line from yt-dlp's stderr tail:
// an "ERROR:" line if there is one, else the last line.
func lastErrorLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(lines[i], "ERROR:"))
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return ""
}

var (
	progressRe = regexp.MustCompile(`\[download\]\s+(\d+(?:\.\d+)?)% of\s+~?\s*([\d.]+)([KMG]iB|B)(?:\s+at\s+(\S+))?(?:\s+ETA\s+(\S+))?`)
	destRe     = regexp.MustCompile(`\[download\] Destination: (.+)$`)
	alreadyRe  = regexp.MustCompile(`\[download\] (.+) has already been downloaded`)
	mergerRe   = regexp.MustCompile(`\[Merger\] Merging formats into "(.+)"`)
	extractRe  = regexp.MustCompile(`\[ExtractAudio\] Destination: (.+)$`)
)

// scanProgress parses yt-dlp --newline output line by line.
func scanProgress(r interface{ Read([]byte) (int, error) }, o Options) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var last Progress
	for sc.Scan() {
		line := sc.Text()
		if m := mergerRe.FindStringSubmatch(line); m != nil {
			// Merged output name — the real final filename.
			last.Filename = m[1]
			if o.OnFilename != nil {
				o.OnFilename(m[1])
			}
			continue
		}
		if m := extractRe.FindStringSubmatch(line); m != nil {
			// Audio-extract output name (e.g. the .mp3).
			last.Filename = m[1]
			if o.OnFilename != nil {
				o.OnFilename(m[1])
			}
			continue
		}
		if m := destRe.FindStringSubmatch(line); m != nil {
			last.Filename = m[1]
			if o.OnFilename != nil {
				o.OnFilename(m[1])
			}
			continue
		}
		if m := alreadyRe.FindStringSubmatch(line); m != nil {
			last.Filename = m[1]
			last.Percent = 100
			if o.OnProgress != nil {
				o.OnProgress(last)
			}
			continue
		}
		if m := progressRe.FindStringSubmatch(line); m != nil {
			pct, _ := strconv.ParseFloat(m[1], 64)
			total := parseSize(m[2], m[3])
			last.Percent = pct
			last.Total = total
			last.Downloaded = int64(pct / 100 * float64(total))
			if m[4] != "" {
				last.Speed = m[4]
			}
			if m[5] != "" {
				last.ETA = m[5]
			}
			if o.OnProgress != nil {
				o.OnProgress(last)
			}
		}
	}
	return sc.Err()
}

func parseSize(num, unit string) int64 {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "KiB":
		return int64(v * 1024)
	case "MiB":
		return int64(v * 1024 * 1024)
	case "GiB":
		return int64(v * 1024 * 1024 * 1024)
	default:
		return int64(v)
	}
}

// HumanSize renders bytes as "84.2 MB" / "1.2 GB" / "512 KB".
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
