package main

// ytdlp.go — wrapper yt-dlp untuk download audio/video dari YouTube.
// Digunakan sebagai alternatif/pengganti theresav API.
// Butuh: pip install yt-dlp  (atau: pip3 install yt-dlp)
//
// Config: CookiesPath di config.go (path ke cookies.txt dari Anya MD-Update)

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ytdlpAvailable cek apakah yt-dlp ada di PATH.
func ytdlpAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// ytdlpRun jalankan yt-dlp dengan timeout — proses yang nge-hang (network stall,
// site anti-bot nahan koneksi, dll) gak boleh menggantung bot selamanya.
// Balikin stdout mentah + error dengan stderr terpotong.
func ytdlpRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timeout %ds", int(timeout.Seconds()))
	}
	if err != nil {
		return "", fmt.Errorf("%s", lastLines(stderr.String(), 3))
	}
	return out.String(), nil
}

// cleanupStrayYTDLP hapus file temp yt-dlp yang bukan output yang diharapkan
// (mis. hasil ekstensi .webm/.m4a karena ffmpeg tidak tersedia, atau sisa .part
// dari download gagal) — cegah file menetap di temp/. globPrefix misal
// "ytdl_<uid>" atau "ytdlv_<uid>", wantExt misal ".mp3".
func cleanupStrayYTDLP(globPrefix, wantExt string) {
	matches, _ := filepath.Glob(filepath.Join("temp", globPrefix+".*"))
	for _, m := range matches {
		if filepath.Ext(m) != wantExt {
			os.Remove(m)
		}
	}
}

// cookiesPath: CookiesPath di config.go hardcoded absolute (warisan dari
// Base-Bot-Wa). Fix: kalau file-nya tidak ada di situ, coba ./cookies.txt —
// biar bot tetap jalan kalau dipindah ke direktori lain.
func cookiesPath() string {
	if CookiesPath != "" {
		if _, err := os.Stat(CookiesPath); err == nil {
			return CookiesPath
		}
	}
	if _, err := os.Stat("cookies.txt"); err == nil {
		return "cookies.txt"
	}
	return ""
}

// ytdlpArgs buat base args yt-dlp, dengan cookies kalau file-nya ada.
func ytdlpBaseArgs() []string {
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--quiet",
		// YouTube sekarang menolak ekstraksi tanpa JS runtime
		// ("HTTP Error 403: Forbidden"); yt-dlp 2025.11+ wajib deno/node/bun
		// untuk men-decrypt signature. node terpasang di sistem ini.
		"--js-runtimes", "node",
	}
	if cp := cookiesPath(); cp != "" {
		args = append(args, "--cookies", cp)
	}
	return args
}

// YtdlpTrack info lagu/video dari yt-dlp.
type YtdlpTrack struct {
	Title    string
	Duration string // "3:42"
	URL      string // original YouTube URL
}

// ytdlpGetInfo ambil judul dan durasi dari URL.
func ytdlpGetInfo(url string) (*YtdlpTrack, error) {
	args := append(ytdlpBaseArgs(),
		"--print", "%(title)s\n%(duration_string)s",
		"--skip-download",
		url,
	)
	out, err := ytdlpRun(30*time.Second, args...)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp info: %s", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	title, dur := "Unknown", "?"
	if len(lines) >= 1 {
		title = lines[0]
	}
	if len(lines) >= 2 {
		dur = lines[1]
	}
	return &YtdlpTrack{Title: title, Duration: dur, URL: url}, nil
}

// ytdlpDownloadAudio download audio dari URL ke file MP3 sementara.
// Return path file MP3.
func ytdlpDownloadAudio(url string) (string, error) {
	if err := os.MkdirAll("temp", 0o755); err != nil {
		return "", err
	}
	uid := uuid.New().String()
	outTemplate := filepath.Join("temp", "ytdl_"+uid+".%(ext)s")
	outPath := filepath.Join("temp", "ytdl_"+uid+".mp3")

	args := append(ytdlpBaseArgs(),
		"-x",
		"--audio-format", "mp3",
		"--audio-quality", "128K",
		"-o", outTemplate,
		url,
	)

	// Retry 1x: YouTube kadang balas HTTP 403 transient (rate-limit IP) —
	// langsung gagal total bikin semua fitur YouTube mati untuk sementara.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		if _, err := ytdlpRun(300*time.Second, args...); err != nil {
			lastErr = fmt.Errorf("yt-dlp audio: %s", err)
			continue
		}
		if _, err := os.Stat(outPath); err != nil {
			cleanupStrayYTDLP("ytdl_"+uid, ".mp3")
			return "", fmt.Errorf("file MP3 tidak ditemukan setelah download")
		}
		return outPath, nil
	}
	cleanupStrayYTDLP("ytdl_"+uid, ".mp3")
	return "", lastErr
}

// ytdlpDownloadVideo download video dari URL ke file MP4 sementara.
// Resolusi max: 480p (sesuai limit video call WA).
// PENTING: paksa codec H.264 (avc1) + AAC (m4a) — YouTube sekarang default ke
// AV1 (itag 397) yang TIDAK bisa diputar WhatsApp ("mp4 gak bisa di play").
// Return path file MP4.
func ytdlpDownloadVideo(url string) (string, error) {
	if err := os.MkdirAll("temp", 0o755); err != nil {
		return "", err
	}
	uid := uuid.New().String()
	outTemplate := filepath.Join("temp", "ytdlv_"+uid+".%(ext)s")
	outPath := filepath.Join("temp", "ytdlv_"+uid+".mp4")

	args := append(ytdlpBaseArgs(),
		"-f", "bestvideo[height<=480][ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=480][ext=mp4][vcodec^=avc1]/best[height<=480][vcodec^=avc1]",
		"--merge-output-format", "mp4",
		"-o", outTemplate,
		url,
	)
	if _, err := ytdlpRun(300*time.Second, args...); err != nil {
		cleanupStrayYTDLP("ytdlv_"+uid, ".mp4")
		return "", fmt.Errorf("yt-dlp video: %s", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		cleanupStrayYTDLP("ytdlv_"+uid, ".mp4")
		return "", fmt.Errorf("file MP4 tidak ditemukan setelah download")
	}
	return outPath, nil
}

// ytdlpSearch cari video YouTube berdasarkan query, return URL pertama.
func ytdlpSearch(query string) (string, error) {
	args := append(ytdlpBaseArgs(),
		"--print", "%(webpage_url)s",
		"--skip-download",
		"--playlist-items", "1",
		"ytsearch1:"+query,
	)
	out, err := ytdlpRun(30*time.Second, args...)
	if err != nil {
		return "", fmt.Errorf("yt-dlp search: %s", err)
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", fmt.Errorf("tidak ditemukan hasil untuk: %s", query)
	}
	return url, nil
}
