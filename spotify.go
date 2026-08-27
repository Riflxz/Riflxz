package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Pencarian lagu buat audio call. Chain lama faa → danzy → nexray sudah
// dibersihkan: danzy (DNS mati) dan nexray (mati) dihapus — sekarang cukup
// api-faa ytplay, dengan 1x retry kalau gagal transien.
const faaYtPlayURL = "https://api-faa.my.id/faa/ytplay"

type spotifyTrack struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Duration    string `json:"duration"`
	Thumbnail   string `json:"thumbnail"`
	Album       string `json:"album"`
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}

// faaYtPlayResponse — response https://api-faa.my.id/faa/ytplay?query=...
type faaYtPlayResponse struct {
	Status bool `json:"status"`
	Result struct {
		Title             string `json:"title"`
		URL               string `json:"url"`
		MP3               string `json:"mp3"`
		Thumbnail         string `json:"thumbnail"`
		DurationTimestamp string `json:"duration_timestamp"`
		Author            string `json:"author"`
	} `json:"result"`
}

// fetchPlayTrack query lagu by judul buat audio call. Primary: api-faa ytplay
// (satu-satunya API; danzy/nexray dihapus karena mati). 1x retry TAPI hanya
// untuk error transien (request gagal / API error) — kalau lagu memang gak
// ketemu, retry cuma buang waktu. Balikin metadata + DownloadURL yang valid.
func fetchPlayTrack(query string) (*spotifyTrack, error) {
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		track, err := fetchFaaYtPlay(query)
		if err == nil {
			return track, nil
		}
		lastErr = err
		// "gak ketemu" = query valid tapi lagu tidak ada → jangan retry.
		if strings.Contains(err.Error(), "gak ketemu") {
			break
		}
		pool.logger.Warn().Int("attempt", attempt).Err(err).Msg("fetchPlayTrack: api-faa gagal")
	}
	return nil, lastErr
}

// fetchFaaYtPlay query ke api-faa (ytplay). Endpoint ini yang dipakai contoh
// JS: data.result.mp3 = URL audio langsung, data.result.duration_timestamp = "3:33".
func fetchFaaYtPlay(query string) (*spotifyTrack, error) {
	endpoint := faaYtPlayURL + "?query=" + url.QueryEscape(query)

	// Timeout 15s: API normalnya balas <10s. Kalau lebih lama, request itu
	// sia-sia — langsung gagal & retry biar total waktu tetap wajar.
	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("request gagal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API status %d", resp.StatusCode)
	}

	var parsed faaYtPlayResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("response gak valid: %w", err)
	}
	if !parsed.Status || parsed.Result.MP3 == "" {
		return nil, fmt.Errorf("lagu \"%s\" gak ketemu", query)
	}
	r := parsed.Result
	return &spotifyTrack{
		Title:       r.Title,
		Artist:      r.Author,
		Duration:    r.DurationTimestamp,
		Thumbnail:   r.Thumbnail,
		URL:         r.URL,
		DownloadURL: r.MP3,
	}, nil
}

// downloadAudio ngambil file audio dari download_url, simpen ke folder temp/.
// Setelah download, otomatis diproses dengan FFmpeg untuk kualitas call optimal:
// - loudnorm: normalisasi volume EBU R128 (suara konsisten, tidak terlalu kecil/besar)
// - high-quality SWR resampler ke 16kHz mono WAV (bypass linear interpolation meowcaller)
// Balikin path WAV yang sudah diproses.
//
// Fix: download_url dari API musik bisa redirect ke halaman HTML (spotidown.app)
// bukan file audio. Deteksi content-type & magic bytes DULU sebelum nyimpen file,
// biar error-nya jelas ("link bukan audio") bukan "ffmpeg gagal" yang membingungkan.
func downloadAudio(downloadURL string) (string, error) {
	// Timeout 45s: MP3 lagu ~3MB, normalnya <10s. Kalau >45s, koneksi
	// bermasalah — gagal cepat daripada bikin user nunggu lama.
	httpClient := &http.Client{Timeout: 45 * time.Second}
	resp, err := httpClient.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("gagal request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	// Cek content-type: kalau HTML, berarti download_url nunjuk halaman web
	// (bukan audio) — langsung tolak dengan pesan yang jelas.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		base := strings.ToLower(strings.Split(ct, ";")[0])
		if strings.Contains(base, "text/html") || strings.Contains(base, "text/plain") {
			return "", fmt.Errorf("link download bukan audio (balikin %s)", base)
		}
	}

	if err := os.MkdirAll("temp", 0o755); err != nil {
		return "", err
	}

	uid := uuid.New().String()
	mp3Path := filepath.Join("temp", uid+".mp3")
	out, err := os.Create(mp3Path)
	if err != nil {
		return "", err
	}

	// Stream: tulis ke file sambil cek magic bytes di awal (maks 512 bytes pertama).
	// Kalau file ternyata HTML (<html>, <!DOCTYPE), hapus & error — bukan audio.
	buf := make([]byte, 512)
	n, rerr := io.ReadFull(resp.Body, buf)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		out.Close()
		os.Remove(mp3Path)
		return "", fmt.Errorf("gagal baca response: %w", rerr)
	}
	head := strings.ToLower(string(buf[:n]))
	if strings.Contains(head, "<html") || strings.Contains(head, "<!doctype") ||
		strings.Contains(head, "<head") || strings.Contains(head, "<body") {
		out.Close()
		os.Remove(mp3Path)
		return "", fmt.Errorf("link download bukan audio (balikin halaman web)")
	}
	if _, err := out.Write(buf[:n]); err != nil {
		out.Close()
		os.Remove(mp3Path)
		return "", err
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(mp3Path)
		return "", fmt.Errorf("gagal nulis file: %w", err)
	}
	out.Close()

	// Proses dengan FFmpeg untuk kualitas call optimal
	wavPath, err := processAudioForCall(mp3Path)
	if err != nil {
		// Fallback: pakai MP3 asli kalau FFmpeg gagal
		pool.logger.Warn().Err(err).Msg("audio processing gagal, pakai MP3 asli")
		return mp3Path, nil
	}
	os.Remove(mp3Path) // hapus MP3 mentah setelah diproses
	return wavPath, nil
}

// processAudioForCall convert audio ke raw 16kHz mono PCM s16le.
//
// Two-step resampling untuk minimalkan aliasing "robot" pada nada tinggi:
//
//	Step 1: source → 48kHz  (upsample ke common rate)
//	Step 2: 48kHz  → 16kHz  (exact 3:1 ratio, decimasi paling bersih)
//
// Rasio 3:1 memungkinkan SWR pakai FIR filter dengan rolloff sempurna
// tanpa phase distortion yang bikin suara metalik/robotic.
//
// Normalisasi pakai dynaudnorm (frame-based) BUKAN loudnorm — loudnorm
// single-pass integrated itu full-scan dan butuh ~50s untuk lagu 5 menit
// (user nunggu "lambat banget"). dynaudnorm hasilnya hampir sama
// (mean ≈ -16 dB, peak ≈ -0.7 dB) tapi selesai dalam <10s.
func processAudioForCall(inputPath string) (string, error) {
	outPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + "_call.raw"

	// Two-step resample: source→48k→16k
	// dynaudnorm di tengah (setelah upsample ke 48k) untuk normalisasi sebelum decimasi
	audioFilter := "dynaudnorm=f=150:g=15," +
		"aresample=48000:resampler=swr:precision=28," +
		"aresample=16000:resampler=swr:precision=28:cutoff=0.92"

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-af", audioFilter,
		"-f", "s16le",
		"-ar", "16000",
		"-ac", "1",
		outPath,
	)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 120*time.Second); err != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("ffmpeg: %s", lastLines(stderr.String(), 3))
	}
	return outPath, nil
}
