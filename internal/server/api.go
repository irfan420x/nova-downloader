// API-backed resolve + download for the NOVA Downloader server.
// The NOVA API (internal/novaapi) is the PRIMARY engine; yt-dlp is
// only a fallback for platforms the API has no endpoint for.
package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irfan420x/nova-downloader/internal/detect"
	"github.com/irfan420x/nova-downloader/internal/engine"
	"github.com/irfan420x/nova-downloader/internal/httpdl"
	"github.com/irfan420x/nova-downloader/internal/novaapi"
	"github.com/irfan420x/nova-downloader/internal/queue"
	"github.com/irfan420x/nova-downloader/internal/yt"
)

// apiProbe resolves rawURL through the NOVA API and maps the result
// onto the probe response shape the UI already renders.
func (s *Server) apiProbe(ctx context.Context, rawURL string) (*probeResp, error) {
	res, err := s.api.Resolve(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	platform := res.Platform
	if platform == novaapi.PlatformDirect {
		// A direct file: one quality, kind "file".
		q := res.Qualities[0]
		return &probeResp{
			Kind: "file", Engine: "api", Platform: platform, URL: rawURL,
			Title: res.Title, Filename: res.Title,
			Size: q.Size, ContentTyp: mimeForExt(q.Ext),
			Streams: []yt.Format{apiQualityToFormat(q)},
		}, nil
	}
	resp := &probeResp{
		Kind: "video", Engine: "api", Platform: platform, URL: rawURL,
		Title: res.Title, Thumbnail: res.Thumbnail,
		// Subtitles come from yt-dlp only; the API returns no subs.
		SubtitlesSupported: false,
	}
	for _, q := range res.Combined() {
		resp.Combined = append(resp.Combined, apiQualityToFormat(q))
	}
	for _, q := range res.VideoOnly() {
		resp.VideoOnly = append(resp.VideoOnly, apiQualityToFormat(q))
	}
	for _, q := range res.AudioOnly() {
		resp.AudioOnly = append(resp.AudioOnly, apiQualityToFormat(q))
	}
	return resp, nil
}

// apiQualityToFormat maps one API quality onto the yt.Format shape
// the UI renders (format picker + size), carrying the stream URL,
// headers and alternates the downloader needs.
func apiQualityToFormat(q novaapi.Quality) yt.Format {
	f := yt.Format{
		ID: q.ID, Ext: q.Ext, FormatNote: q.Label,
		StreamURL: q.URL, StreamHeaders: q.Headers, Alternates: q.Alternates,
	}
	if q.Size > 0 {
		sz := q.Size
		f.Filesize = &sz
	}
	// Height from labels like "720p" / "1080p60".
	if h := parseHeight(q.Label); h > 0 {
		f.Height = h
		f.Resolution = fmt.Sprintf("%dp", h)
	}
	// Bitrate from audio labels like "aac 160k".
	if q.AudioOnly {
		if tbr := parseBitrateK(q.Label); tbr > 0 {
			f.TBR = &tbr
		}
	}
	switch {
	case q.VideoOnly:
		f.VCodec, f.ACodec = "avc1", "none"
	case q.AudioOnly:
		f.VCodec, f.ACodec = "none", "mp4a"
	default:
		f.VCodec, f.ACodec = "avc1", "mp4a"
	}
	return f
}

func parseHeight(label string) int {
	// "720p" / "1080p60" / "720p60fps" — digits before the first 'p'.
	i := strings.IndexByte(label, 'p')
	if i <= 0 {
		return 0
	}
	n, err := strconv.Atoi(label[:i])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func parseBitrateK(label string) float64 {
	// "aac 160k" / "160k" — the number before a trailing 'k'.
	fields := strings.Fields(label)
	for _, f := range fields {
		if strings.HasSuffix(f, "k") {
			if n, err := strconv.ParseFloat(strings.TrimSuffix(f, "k"), 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	case "mov":
		return "video/quicktime"
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "ogg":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "pdf":
		return "application/pdf"
	case "zip":
		return "application/zip"
	default:
		return ""
	}
}

// SafeFilename builds a safe output filename from a title and
// extension: unsafe characters become "_", length is capped, and an
// empty title falls back to "download".
func SafeFilename(title, ext string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 32 || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(strings.TrimSpace(b.String()), ".")
	if name == "" {
		name = "download"
	}
	// Cap at ~180 bytes so the full path stays well under OS limits.
	for len(name) > 180 {
		name = name[:len(name)-1]
	}
	name = strings.TrimRight(name, ". ")
	if ext != "" {
		ext = strings.TrimPrefix(ext, ".")
		if !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(ext)) {
			name += "." + ext
		}
	}
	return name
}

// ensureAPIStream re-resolves a job whose in-memory stream data is
// gone (e.g. resumed after a restart — stream URLs expire and
// headers are never persisted). It picks the same quality ID when
// still available, else the best matching quality.
func (s *Server) ensureAPIStream(ctx context.Context, j *queue.Job) error {
	if j.StreamURL != "" {
		return nil
	}
	res, err := s.api.Resolve(ctx, j.URL)
	if err != nil {
		return fmt.Errorf("re-resolve failed: %w", err)
	}
	var q *novaapi.Quality
	if j.FormatID != "" {
		q = res.Find(j.FormatID)
	}
	if q == nil {
		if j.AudioOnly {
			q = res.BestAudio()
		} else if c := res.Combined(); len(c) > 0 {
			q = &c[0]
		} else if len(res.Qualities) > 0 {
			q = &res.Qualities[0]
		}
	}
	if q == nil {
		return errors.New("API returned no usable stream on re-resolve")
	}
	j.StreamURL = q.URL
	j.StreamHeaders = q.Headers
	j.Alternates = q.Alternates
	j.StreamExt = q.Ext
	if j.Filename == "" {
		j.Filename = SafeFilename(res.Title, q.Ext)
	}
	if j.Merged {
		if a := res.BestAudio(); a != nil {
			j.AudioStreamURL = a.URL
			j.AudioStreamHeaders = a.Headers
			j.AudioStreamExt = a.Ext
		}
	}
	return nil
}

// outputExt is the final file extension for an API job: an HLS
// playlist downloads as media (.mp4 — never ".m3u8", which would make
// ffmpeg write a playlist + segments instead of one file), mp3 jobs
// as .mp3.
func outputExt(streamExt string, audioOnly bool) string {
	if audioOnly {
		return "mp3"
	}
	if strings.EqualFold(streamExt, "m3u8") {
		return "mp4"
	}
	return streamExt
}

// runAPIJob downloads an API-resolved job: direct stream, HLS, mp3
// extraction, or video+audio merge, depending on the job.
func (s *Server) runAPIJob(ctx context.Context, j *queue.Job, report func(queue.Progress)) error {
	if err := s.ensureAPIStream(ctx, j); err != nil {
		return err
	}
	if err := os.MkdirAll(j.OutDir, 0o755); err != nil {
		return err
	}
	name := j.Filename
	if name == "" {
		title := j.Title
		if strings.EqualFold(j.StreamExt, "m3u8") {
			title = strings.TrimSuffix(title, filepath.Ext(title))
		}
		name = SafeFilename(title, outputExt(j.StreamExt, j.AudioOnly))
	}
	dest := filepath.Join(j.OutDir, name)

	progress := func(p httpdl.Progress) {
		pct := 0.0
		if p.Total > 0 {
			pct = float64(p.Done) / float64(p.Total) * 100
		}
		report(queue.Progress{
			Percent: pct, Done: p.Done, Total: p.Total,
			Speed: yt.HumanSize(p.SpeedBps) + "/s", Filename: name,
		})
	}

	// HLS quality (ext m3u8 — SoundCloud transcodings, /hls links).
	// The built-in segment downloader goes FIRST: it is resumable,
	// reports honest progress, and works where ffmpeg's HLS/TLS
	// handling fails (seen live: ffmpeg 8.1.2 rejects the Mux test
	// stream's segments, the built-in downloader fetches them fine).
	// ffmpeg is the fallback — it alone handles encrypted segments.
	// The output takes the segment container's extension (.ts for
	// MPEG-TS segments — concatenating them is a valid .ts file, not
	// an .mp4); ffmpeg remuxes to real .mp4 when it runs.
	if strings.EqualFold(j.StreamExt, "m3u8") {
		if j.AudioOnly {
			// mp3 straight from the playlist — no intermediate file.
			if engine.FFmpeg() == "" {
				return errors.New("ffmpeg not found — mp3 extraction needs ffmpeg")
			}
			mp3Dest := strings.TrimSuffix(dest, filepath.Ext(dest)) + ".mp3"
			if err := ffmpegConvertMP3FromURL(engine.FFmpeg(), j.StreamURL, j.StreamHeaders, mp3Dest); err != nil {
				return err
			}
			report(queue.Progress{Percent: 100, Filename: filepath.Base(mp3Dest)})
			return nil
		}
		segExt := "mp4"
		if e, err := httpdl.HLSSegmentExt(ctx, j.StreamURL, j.StreamHeaders); err == nil && e != "" {
			segExt = strings.TrimPrefix(e, ".")
		}
		title := j.Title
		if strings.EqualFold(j.StreamExt, "m3u8") {
			title = strings.TrimSuffix(title, filepath.Ext(title))
		}
		hlsDest := filepath.Join(j.OutDir, SafeFilename(title, segExt))
		name = filepath.Base(hlsDest) // progress reports the real file
		hlsErr := httpdl.DownloadHLS(ctx, j.StreamURL, hlsDest, j.StreamHeaders, progress)
		if hlsErr == nil {
			report(queue.Progress{Percent: 100, Filename: filepath.Base(hlsDest)})
			return nil
		}
		if ff := engine.FFmpeg(); ff != "" {
			// Built-in failed (e.g. encrypted segments) — ffmpeg
			// remuxes to a real .mp4.
			if ferr := s.ffmpegHLS(ctx, ff, j, dest, report); ferr == nil {
				return nil
			}
		}
		return hlsErr
	}

	// mp3: download the audio stream, convert with ffmpeg.
	if j.AudioOnly {
		if engine.FFmpeg() == "" {
			return errors.New("ffmpeg not found — mp3 extraction needs ffmpeg")
		}
		audioDest, err := s.downloadStream(ctx, j, j.StreamURL, j.StreamHeaders, j.Alternates, dest+".audio", progress)
		if err != nil {
			return err
		}
		defer os.Remove(audioDest)
		mp3Dest := strings.TrimSuffix(dest, filepath.Ext(dest)) + ".mp3"
		if err := ffmpegConvertMP3(engine.FFmpeg(), audioDest, mp3Dest); err != nil {
			return err
		}
		report(queue.Progress{Percent: 100, Filename: filepath.Base(mp3Dest)})
		return nil
	}

	// Video-only + merge: download video + best audio, merge with ffmpeg.
	if j.Merged && j.AudioStreamURL != "" {
		if engine.FFmpeg() == "" {
			// No ffmpeg: download the video alone, labelled honestly.
			// The UI warns about this before queuing.
			return s.downloadStreamTo(ctx, j, dest, progress)
		}
		videoTmp, err := s.downloadStream(ctx, j, j.StreamURL, j.StreamHeaders, j.Alternates, dest+".video", progress)
		if err != nil {
			return err
		}
		defer os.Remove(videoTmp)
		audioTmp, err := s.downloadStream(ctx, j, j.AudioStreamURL, j.AudioStreamHeaders, nil, dest+".audio", progress)
		if err != nil {
			return err
		}
		defer os.Remove(audioTmp)
		report(queue.Progress{Percent: 99, Speed: "merging…", Filename: name})
		merged := mergeDest(dest, j.StreamExt, j.AudioStreamExt)
		if err := ffmpegMerge(engine.FFmpeg(), videoTmp, audioTmp, merged); err != nil {
			return err
		}
		report(queue.Progress{Percent: 100, Filename: filepath.Base(merged)})
		return nil
	}

	return s.downloadStreamTo(ctx, j, dest, progress)
}

// downloadStreamTo downloads the job's stream to dest.
func (s *Server) downloadStreamTo(ctx context.Context, j *queue.Job, dest string, progress func(httpdl.Progress)) error {
	_, err := s.downloadStream(ctx, j, j.StreamURL, j.StreamHeaders, j.Alternates, dest, progress)
	return err
}

// downloadStream downloads one stream URL (+ alternates) to destPath
// and returns the final path (httpdl may add " (1)" on collisions).
func (s *Server) downloadStream(ctx context.Context, j *queue.Job, streamURL string, headers map[string]string, alternates []string, destPath string, progress func(httpdl.Progress)) (string, error) {
	return httpdl.DownloadWithHeaders(ctx, streamURL, destPath, headers, alternates, progress)
}

// mergeDest picks the merged output name: .mp4 when both streams are
// MP4-family (copy-merge is safe), .mkv otherwise.
func mergeDest(dest, videoExt, audioExt string) string {
	base := strings.TrimSuffix(dest, filepath.Ext(dest))
	v, a := strings.ToLower(videoExt), strings.ToLower(audioExt)
	if (v == "mp4" || v == "") && (a == "m4a" || a == "mp4" || a == "") {
		return base + ".mp4"
	}
	return base + ".mkv"
}

// ffmpegHLS downloads an HLS playlist with ffmpeg (handles variant
// selection + segments itself). -allowed_extensions ALL is needed
// for streams whose segment URLs carry no recognised extension.
func (s *Server) ffmpegHLS(ctx context.Context, ff string, j *queue.Job, dest string, report func(queue.Progress)) error {
	args := []string{"-y", "-v", "error", "-allowed_extensions", "ALL"}
	for k, v := range j.StreamHeaders {
		if strings.EqualFold(k, "user-agent") {
			args = append(args, "-user_agent", v)
		} else if strings.EqualFold(k, "referer") {
			args = append(args, "-referer", v)
		}
	}
	args = append(args, "-i", j.StreamURL, "-c", "copy", dest)
	cmd := exec.CommandContext(ctx, ff, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg HLS download failed: %s", firstLineOr(string(out), err.Error()))
	}
	report(queue.Progress{Percent: 100, Filename: filepath.Base(dest)})
	return nil
}

// ffmpegConvertMP3FromURL extracts audio to mp3 straight from a
// stream URL (HLS playlists) without an intermediate download.
func ffmpegConvertMP3FromURL(ff, streamURL string, headers map[string]string, out string) error {
	args := []string{"-y", "-v", "error"}
	for k, v := range headers {
		if strings.EqualFold(k, "user-agent") {
			args = append(args, "-user_agent", v)
		} else if strings.EqualFold(k, "referer") {
			args = append(args, "-referer", v)
		}
	}
	args = append(args, "-i", streamURL, "-vn", "-c:a", "libmp3lame", "-q:a", "2", out)
	cmd := exec.Command(ff, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg mp3 conversion failed: %s", firstLineOr(string(b), err.Error()))
	}
	return nil
}

// ffmpegConvertMP3 extracts audio to mp3.
func ffmpegConvertMP3(ff, in, out string) error {
	cmd := exec.Command(ff, "-y", "-v", "error", "-i", in, "-vn", "-c:a", "libmp3lame", "-q:a", "2", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg mp3 conversion failed: %s", firstLineOr(string(b), err.Error()))
	}
	return nil
}

// ffmpegMerge merges a video-only + audio-only file with stream copy.
func ffmpegMerge(ff, video, audio, out string) error {
	cmd := exec.Command(ff, "-y", "-v", "error", "-i", video, "-i", audio, "-c", "copy", "-map", "0:v:0", "-map", "1:a:0", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge failed: %s", firstLineOr(string(b), err.Error()))
	}
	return nil
}

func firstLineOr(out, fallback string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return fallback
	}
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		return strings.TrimSpace(out[:i])
	}
	return out
}

// isAPIError reports whether err came from the API itself (as opposed
// to a transport failure reaching it).
func isAPIError(err error) bool {
	var apiErr *novaapi.APIError
	return errors.As(err, &apiErr)
}

// probeKindFileViaAPI tries the API /direct endpoint for a direct
// file; on a transport failure (API unreachable) it falls back to the
// local HEAD + download path so plain files still work offline.
func (s *Server) probeDirectFile(ctx context.Context, rawURL string, head *detect.HeadResult) (*probeResp, error) {
	resp, err := s.apiProbe(ctx, rawURL)
	if err == nil {
		return resp, nil
	}
	if isAPIError(err) {
		// The API itself refused it (e.g. "web page, not a file") —
		// that verdict is honest; do not second-guess it locally.
		return nil, err
	}
	// API unreachable: local direct download.
	if head == nil {
		return nil, err
	}
	return &probeResp{
		Kind: "file", Engine: "file", URL: rawURL,
		Title: head.Filename, Filename: head.Filename,
		Size: head.Size, ContentTyp: head.ContentType,
	}, nil
}
