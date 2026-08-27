package main

// playch.go — !playch <judul>: cari lagu YouTube, download MP3, konversi ke
// OGG Opus, lalu kirim ke saluran (channel) pilihan. Owner only (guard router).
//
// Alur: !playch <judul> → bot tampilkan daftar saluran (button/dropdown) →
// klik/ketik nomor → proses & kirim ke saluran terpilih. Kalau hanya ada 1
// saluran yang bisa kirim, langsung proses tanpa picker.
//
// Port dari plugin playch Velyon + pola kirim-channel yang sudah jalan di
// bot ini (sendMediaToNewsletter, channel_features.go):
//   - search: yts()        → youtubeSearchSiputzx / youtubeSearchHTML
//   - audio:  cuki (butuh key) → yt-dlp primary / siputzx fallback
//   - konversi: libopus 32k mono 48k + -application voip -vbr on
//     -compression_level 10 -frame_duration 20 (tanpa efek audio)
//   - KIRIM KE CHANNEL: UploadNewsletter (unencrypted) + MediaHandle —
//     waClient.Upload biasa (encrypted) menghasilkan media yang TIDAK bisa
//     diputar di channel WA. Audio biasa (tanpa PTT/waveform): channel tidak
//     mendukung voice note.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// pendingPlayCh menyimpan query lagu sementara sampai user memilih saluran
// (lewat tombol/ketik nomor). Key = sender number, TTL 3 menit.
type pendingPlayCh struct {
	query     string
	channels  []channelEntry // daftar saluran yang ditampilkan di picker
	expiresAt time.Time
	// Fix: anti race — dua pesan user dalam jarak dekat bisa baca pending yang
	// sama sebelum salah satunya sempat delete dari map → proses double.
	mu      sync.Mutex
	claimed bool
}

var (
	playChPendingMu sync.Mutex
	playChPending   = map[string]*pendingPlayCh{}
)

func cleanExpiredPlayChPending() {
	playChPendingMu.Lock()
	defer playChPendingMu.Unlock()
	now := time.Now()
	for k, v := range playChPending {
		if now.After(v.expiresAt) {
			delete(playChPending, k)
		}
	}
}

func handlePlayCh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	user := senderUser(evt)
	cleanExpiredPlayChPending()

	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("🎵 *PLAY CH*\n\nContoh:\n`%splaych nama lagu`", Prefix))
		return
	}

	// ── Cek pending state (pilihan saluran dari tombol/ketik nomor) ──────────
	playChPendingMu.Lock()
	pending, hasPending := playChPending[user]
	playChPendingMu.Unlock()

	if hasPending && time.Now().After(pending.expiresAt) {
		playChPendingMu.Lock()
		delete(playChPending, user)
		playChPendingMu.Unlock()
		hasPending = false
		sendText(ctx, chat, "⌛ Sesi kadaluarsa. Ulangi *"+Prefix+"playch*.")
	}

	if hasPending && isNumericStr(query) {
		// Pilih saluran dengan nomor dari daftar yang ditampilkan.
		idx, err := strconv.Atoi(query)
		if err != nil || idx < 0 || idx >= len(pending.channels) {
			sendText(ctx, chat, fmt.Sprintf("❌ Pilih nomor 0–%d.", len(pending.channels)-1))
			return
		}
		processPlayCh(ctx, evt, pending.query, pending.channels[idx])
		return
	}

	// ── Mulai flow baru: ambil daftar saluran yang bisa kirim ────────────────
	reactMsg(ctx, evt, "⏳")

	allChannels, err := fetchChannelList(ctx)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar saluran: "+err.Error())
		return
	}
	if len(allChannels) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot tidak mengikuti saluran manapun.")
		return
	}

	// Filter hanya yang bisa kirim (owner atau admin)
	var writeable []channelEntry
	for _, c := range allChannels {
		if c.Role == "owner" || c.Role == "admin" {
			writeable = append(writeable, c)
		}
	}
	if len(writeable) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot bukan admin/owner di saluran manapun.\n"+
			"Bot harus jadi admin saluran untuk bisa kirim.")
		return
	}

	newPending := &pendingPlayCh{
		query:     query,
		channels:  writeable,
		expiresAt: time.Now().Add(3 * time.Minute),
	}

	// Kalau hanya 1 saluran yang bisa kirim, langsung proses tanpa picker.
	if len(writeable) == 1 {
		playChPendingMu.Lock()
		playChPending[user] = newPending
		playChPendingMu.Unlock()
		processPlayCh(ctx, evt, query, writeable[0])
		return
	}

	playChPendingMu.Lock()
	playChPending[user] = newPending
	playChPendingMu.Unlock()

	reactMsg(ctx, evt, "✅")
	sendChannelList(ctx, chat, writeable,
		fmt.Sprintf("🎵 *%s*\n\nPilih saluran tujuan:", query), "playch")
}

// processPlayCh jalankan alur lengkap: cari video → download audio → konversi
// OGG → kirim ke saluran target (UploadNewsletter + MediaHandle).
func processPlayCh(ctx context.Context, evt *events.Message, query string, target channelEntry) {
	chat := evt.Info.Chat

	playChPendingMu.Lock()
	delete(playChPending, senderUser(evt))
	playChPendingMu.Unlock()

	reactMsg(ctx, evt, "⏳")

	// ── Cari video YouTube ──
	results, err := youtubeSearchSiputzx(query)
	if err != nil {
		results, err = youtubeSearchHTML(query)
	}
	if err != nil || len(results) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video tidak ditemukan!")
		return
	}
	videoID := results[0].id
	title := results[0].title
	if title == "" {
		title = query
	}
	ytURL := "https://www.youtube.com/watch?v=" + videoID
	sendText(ctx, chat, fmt.Sprintf("⏳ Mempersiapkan *%s*...", title))

	// ── Audio MP3: yt-dlp primary → siputzx fallback ──
	audioData, err := fetchPlayChAudio(ytURL, videoID)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download audio: "+err.Error())
		return
	}

	// ── Konversi ke OGG Opus tanpa efek ──
	oggPath, _, err := convertToOpusPtt(audioData)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal konversi audio: "+err.Error())
		return
	}
	defer os.Remove(oggPath)

	oggData, err := os.ReadFile(oggPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil konversi: "+err.Error())
		return
	}

	// Kirim ke channel — WAJIB UploadNewsletter (unencrypted) + MediaHandle:
	// pola yang sama dengan sendMediaToNewsletter yang sudah terbukti jalan.
	// Upload biasa (encrypted) ke newsletter → media tidak bisa diputar WA.
	up, err := waClient.UploadNewsletter(ctx, oggData, whatsmeow.MediaAudio)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload audio: "+err.Error())
		return
	}
	_, err = waClient.SendMessage(ctx, target.JID, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:        proto.String(up.URL),
			DirectPath: proto.String(up.DirectPath),
			FileSHA256: up.FileSHA256,
			FileLength: &up.FileLength,
			Mimetype:   proto.String("audio/ogg; codecs=opus"),
		},
	}, whatsmeow.SendRequestExtra{MediaHandle: up.Handle})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim ke saluran: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ Berhasil diputar di saluran *%s*!\n\n🎵 *%s*", target.Name, title))
}

// fetchPlayChAudio download MP3 lagu: yt-dlp primary, siputzx ummy fallback.
func fetchPlayChAudio(ytURL, videoID string) ([]byte, error) {
	if ytdlpAvailable() {
		if p, err := ytdlpDownloadAudio(ytURL); err == nil {
			defer os.Remove(p)
			if data, rerr := os.ReadFile(p); rerr == nil {
				return data, nil
			} else {
				pool.logger.Warn().Err(rerr).Msg("playch: baca mp3 yt-dlp gagal")
			}
		} else {
			pool.logger.Warn().Err(err).Msg("playch: yt-dlp audio gagal, coba siputzx")
		}
	}
	mp3URL, err := ytFetchMP3URL(videoID)
	if err != nil {
		return nil, err
	}
	// googlevideo (CDN YouTube) menolak tanpa Referer; UA desktop + referer
	// YouTube supaya CDN tidak balas halaman error/403.
	return dlGet(mp3URL, map[string]string{
		"Referer":    "https://www.youtube.com/",
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
}

// convertToOpusPtt konversi MP3 → OGG Opus voice note, persis parameter
// playch Velyon (yang terbukti bisa diputar WhatsApp):
// -vn -map_metadata -1 -ac 1 -ar 48000 -c:a libopus -b:a 32k -application
// voip -vbr on -compression_level 10 -frame_duration 20 -f ogg.
// Return path file .ogg (durasi tidak dipakai lagi — Velyon tidak kirim
// seconds; waveform yang jadi penentu playability).
func convertToOpusPtt(audioData []byte) (string, uint32, error) {
	if err := os.MkdirAll("temp", 0o755); err != nil {
		return "", 0, err
	}
	uid := uuid.New().String()
	inPath := filepath.Join("temp", "pch_in_"+uid+".mp3")
	outPath := filepath.Join("temp", "pch_out_"+uid+".ogg")
	if err := os.WriteFile(inPath, audioData, 0o644); err != nil {
		return "", 0, err
	}
	defer os.Remove(inPath)

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath,
		"-vn",
		"-map_metadata", "-1",
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "32k",
		"-application", "voip",
		"-vbr", "on",
		"-compression_level", "10",
		"-frame_duration", "20",
		"-f", "ogg",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := runCmdTimeout(cmd, 120*time.Second); err != nil {
		os.Remove(outPath)
		return "", 0, fmt.Errorf("ffmpeg: %s", lastLines(stderr.String(), 3))
	}
	return outPath, 0, nil
}

// probeAudioDuration ambil durasi audio (detik) via ffprobe; 0 kalau gagal.
func probeAudioDuration(path string) uint32 {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &f); err != nil || f < 0 {
		return 0
	}
	return uint32(f)
}
