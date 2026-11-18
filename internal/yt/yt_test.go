package yt

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleJSON = `{
  "id": "aqz-KE-bpKQ",
  "title": "Big Buck Bunny 4K 60fps",
  "uploader": "Blender Foundation",
  "webpage_url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
  "thumbnail": "https://i.ytimg.com/vi/aqz-KE-bpKQ/hqdefault.jpg",
  "duration": 635.0,
  "_type": "video",
  "extractor": "youtube",
  "formats": [
    {"format_id": "399", "ext": "mp4", "resolution": "1080x1080", "width": 1080, "height": 1080, "fps": 60, "vcodec": "av01.0.08M.08", "acodec": "none", "filesize": 100000000, "format_note": "1080p60", "tbr": 1200},
    {"format_id": "248", "ext": "webm", "resolution": "1080x1080", "width": 1080, "height": 1080, "fps": 60, "vcodec": "vp9", "acodec": "none", "filesize": 90000000, "format_note": "1080p60", "tbr": 1100},
    {"format_id": "22", "ext": "mp4", "resolution": "1280x720", "width": 1280, "height": 720, "fps": 30, "vcodec": "avc1.64001F", "acodec": "mp4a.40.2", "filesize": 50000000, "format_note": "720p", "tbr": 600},
    {"format_id": "18", "ext": "mp4", "resolution": "640x360", "width": 640, "height": 360, "fps": 30, "vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "filesize": 20000000, "format_note": "360p", "tbr": 250},
    {"format_id": "140", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "filesize": 8000000, "tbr": 128},
    {"format_id": "251", "ext": "webm", "vcodec": "none", "acodec": "opus", "filesize": 6000000, "tbr": 160}
  ]
}`

func TestParseInfoAndGrouping(t *testing.T) {
	var info Info
	if err := json.NewDecoder(strings.NewReader(sampleJSON)).Decode(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Title != "Big Buck Bunny 4K 60fps" {
		t.Fatalf("title = %q", info.Title)
	}
	if info.IsPlaylist() {
		t.Fatal("video reported as playlist")
	}
	comb := info.CombinedFormats()
	if len(comb) != 2 {
		t.Fatalf("combined = %d, want 2", len(comb))
	}
	if comb[0].Height != 720 || comb[1].Height != 360 {
		t.Fatalf("combined order wrong: %v", comb)
	}
	if got := info.BestFormat(); got != "22" {
		t.Fatalf("best = %q, want 22", got)
	}
	vo := info.VideoOnlyFormats()
	if len(vo) != 2 || vo[0].ID != "399" {
		t.Fatalf("video-only = %v", vo)
	}
	ao := info.AudioOnlyFormats()
	if len(ao) != 2 || ao[0].ID != "251" {
		t.Fatalf("audio-only = %v (want 251 first, best bitrate)", ao)
	}
}

func TestMergedOptions(t *testing.T) {
	var info Info
	if err := json.NewDecoder(strings.NewReader(sampleJSON)).Decode(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sample has real combined formats (22, 18) AND separate streams.
	if got := info.CombinedFormats(); len(got) != 2 {
		t.Fatalf("combined = %d, want 2", len(got))
	}
	// Merged options are built from the video-only heights (1080p only
	// in this sample — 399/248 are both 1080p).
	merged := info.MergedOptions()
	if len(merged) != 1 {
		t.Fatalf("merged = %d, want 1 (1080p)", len(merged))
	}
	if merged[0].Height != 1080 {
		t.Fatalf("merged height = %d, want 1080", merged[0].Height)
	}
	if !strings.Contains(merged[0].ID, "bv*[height<=1080]+ba") {
		t.Fatalf("merged id = %q", merged[0].ID)
	}
	if merged[0].Size() == 0 {
		t.Fatal("merged size estimate is 0")
	}
	// And when a video has NO combined formats at all (modern YouTube),
	// merged options are what the picker shows.
	var dashOnly Info
	if err := json.NewDecoder(strings.NewReader(sampleJSON)).Decode(&dashOnly); err != nil {
		t.Fatalf("decode: %v", err)
	}
	dashOnly.Formats = dashOnly.VideoOnlyFormats()
	dashOnly.Formats = append(dashOnly.Formats, dashOnly.AudioOnlyFormats()...)
	if got := dashOnly.CombinedFormats(); len(got) != 0 {
		t.Fatalf("dash-only combined = %d, want 0", len(got))
	}
	if got := dashOnly.MergedOptions(); len(got) != 1 {
		t.Fatalf("dash-only merged = %d, want 1", len(got))
	}
}

func TestFormatLabel(t *testing.T) {
	f := Format{Height: 1080, FPS: 60, Ext: "mp4", FormatNote: "1080p60"}
	size := int64(84_200_000)
	f.Filesize = &size
	lbl := f.Label()
	if !strings.Contains(lbl, "1080p60") || !strings.Contains(lbl, "mp4") || !strings.Contains(lbl, "80.3 MB") {
		t.Fatalf("label = %q", lbl)
	}
}

func TestScanProgressMergerFilename(t *testing.T) {
	lines := `[download] Destination: /tmp/BBB.f396.mp4
[download] 100.0% of   15.00MiB at    2.00MiB/s ETA 00:00
[Merger] Merging formats into "/tmp/BBB.mp4"
`
	var filename string
	_ = scanProgress(strings.NewReader(lines), Options{
		OnProgress: func(p Progress) {},
		OnFilename: func(n string) { filename = n },
	})
	if filename != "/tmp/BBB.mp4" {
		t.Fatalf("filename = %q, want merged output name", filename)
	}
}

func TestScanProgress(t *testing.T) {
	lines := `[download] Destination: /tmp/Big Buck Bunny.mp4
[download]   0.0% of   10.00MiB at  500.00KiB/s ETA 00:20
[download]  45.2% of   10.00MiB at    1.23MiB/s ETA 00:04
[download] 100.0% of   10.00MiB at    2.00MiB/s ETA 00:00
`
	var got []Progress
	var filename string
	err := scanProgress(strings.NewReader(lines), Options{
		OnProgress: func(p Progress) { got = append(got, p) },
		OnFilename: func(n string) { filename = n },
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if filename != "/tmp/Big Buck Bunny.mp4" {
		t.Fatalf("filename = %q", filename)
	}
	if len(got) != 3 {
		t.Fatalf("progress updates = %d, want 3", len(got))
	}
	if got[1].Percent != 45.2 || got[1].Total != 10*1024*1024 || got[1].ETA != "00:04" {
		t.Fatalf("mid progress = %+v", got[1])
	}
	if got[2].Percent != 100 {
		t.Fatalf("final percent = %v", got[2].Percent)
	}
}

func TestScanProgressAlreadyDownloaded(t *testing.T) {
	lines := "[download] /tmp/x.mp4 has already been downloaded\n"
	var got []Progress
	_ = scanProgress(strings.NewReader(lines), Options{
		OnProgress: func(p Progress) { got = append(got, p) },
	})
	if len(got) != 1 || got[0].Percent != 100 {
		t.Fatalf("already-downloaded not reported: %+v", got)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0: "0 B", 512: "512 B", 2048: "2.0 KB",
		84_200_000: "80.3 MB", 2_147_483_648: "2.0 GB",
	}
	for in, want := range cases {
		if got := HumanSize(in); got != want {
			t.Fatalf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
