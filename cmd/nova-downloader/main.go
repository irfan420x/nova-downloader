// Command nova-downloader is the NOVA Desktop Downloader: a single
// lightweight Go binary that serves its UI on localhost. Paste any public
// link, pick a quality, download it — video via yt-dlp, files via a
// built-in resumable HTTP downloader. No ads, no trackers, no account.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/irfan420x/nova-downloader/internal/engine"
	"github.com/irfan420x/nova-downloader/internal/queue"
	"github.com/irfan420x/nova-downloader/internal/server"
)

const version = "1.2.0"

func main() {
	port := flag.Int("port", 8765, "localhost port for the web UI")
	open := flag.Bool("open", true, "open the UI in the default browser")
	dir := flag.String("dir", "", "download directory (default ~/Downloads/NOVA)")
	flag.Parse()

	dataDir, err := engine.DataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot find config dir:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create config dir:", err)
		os.Exit(1)
	}

	settings := server.LoadSettings(dataDir)
	if *dir != "" {
		settings.OutDir = *dir
	}
	if err := os.MkdirAll(settings.OutDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create download dir:", err)
		os.Exit(1)
	}

	mgr := queue.New(settings.OutDir, settings.MaxConcurrent,
		filepath.Join(dataDir, "jobs.json"), nil)
	srv := server.New(mgr, settings, dataDir)
	mgr.SetRunner(srv.Runner)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	url := "http://" + addr + "/"
	fmt.Printf("NOVA Downloader %s\n", version)
	fmt.Printf("UI:      %s\n", url)
	fmt.Printf("Save to: %s\n", settings.OutDir)
	if ytPath := engine.Find(); ytPath != "" {
		if ver, err := engine.Version(ytPath); err == nil {
			fmt.Printf("Engine:  yt-dlp %s (%s)\n", ver, ytPath)
		}
	} else {
		fmt.Println("Engine:  yt-dlp NOT FOUND — video downloads need it; install from the Settings panel in the UI.")
	}
	if engine.FFmpeg() != "" {
		fmt.Println("ffmpeg:  found (mp3 extraction + subtitles work)")
	} else {
		fmt.Println("ffmpeg:  not found (mp3 extraction needs ffmpeg; video downloads still work)")
	}

	if *open {
		openBrowser(url)
	}

	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "server error:", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	fmt.Println("\nShutting down — unfinished downloads stay queued and resume next start.")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}
