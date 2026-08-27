package main

// downloader.go — plugin downloader: TikTok, YouTube, Twitter, Instagram, Facebook, SoundCloud
// Port dari Anya MD plugins ke Go (whatsmeow).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── HTTP helper ─────────────────────────────────────────────────────────────

// dlClient — client HTTP global. Timeout 180s: banyak dipakai buat download
// media besar (video TikTok HD bisa 50MB+; di koneksi ~500KB/s itu butuh
// 100s+). 60s dulu bikin !tt gagal "context deadline exceeded".
var dlClient = &http.Client{Timeout: 180 * time.Second}

// readAllLimit baca body dengan batas maksimum — response yang lebih besar dari
// max dianggap error (bukan dipotong diam-diam seperti io.LimitReader polos).
// Fix: dlGet/dlPost sebelumnya io.ReadAll tanpa limit → media raksasa bisa
// bikin OOM.
func readAllLimit(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response terlalu besar (>%d MB)", max>>20)
	}
	return data, nil
}

// ─── SSRF-safe HTTP helper ────────────────────────────────────────────────────
// Dipakai command yang mem-fetch URL ARBITRER dari user (!fetch, !source).
// dlGet biasa TIDAK aman dipakai untuk itu: user bisa minta bot fetch
// http://169.254.169.254 (metadata cloud), http://127.0.0.1:port (service
// internal), atau http://192.168.x.x (network lokal) — SSRF.
//
// dlGetSafe: resolve hostname DULU, blokir IP private/link-local/loopback,
// lalu dial langsung ke IP hasil resolve (bukan resolve ulang oleh kernel)
// supaya DNS-rebinding tidak bisa mem-bypass blokir.

// ipDisallowed true kalau IP termasuk kategori yang wajib diblokir.
func ipDisallowed(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() {
		return true // nil, loopback, link-local, multicast, unspecified, dll
	}
	// IsGlobalUnicast() masih true untuk 100.64.0.0/10 (CGNAT, RFC 6598) &
	// 198.18.0.0/15 (benchmark, RFC 2544) — dua-duanya bukan target publik yang
	// sah dan bisa menunjuk ke infra carrier/provider. Blokir eksplisit.
	if ip4 := ip.To4(); ip4 != nil {
		if (ip4[0] == 100 && ip4[1]&0xC0 == 64) || (ip4[0] == 198 && ip4[1]&0xFE == 18) {
			return true
		}
	}
	return ip.IsPrivate() || ip.IsLoopback()
}

// resolveBlocked cek apakah semua IP hasil resolve nama host itu terlarang.
func resolveBlocked(host string) ([]net.IP, error) {
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	blocked := []net.IP{}
	for _, a := range addrs {
		if ip4 := a.To4(); ip4 != nil {
			blocked = append(blocked, ip4)
		} else if ip6 := a.To16(); ip6 != nil {
			blocked = append(blocked, ip6)
		}
	}
	if len(blocked) == 0 {
		return nil, fmt.Errorf("gak bisa resolve host %q", host)
	}
	for _, ip := range blocked {
		if ipDisallowed(ip) {
			return nil, fmt.Errorf("alamat internal diblokir (%s)", ip)
		}
	}
	return blocked, nil
}

var safeDialer = &net.Dialer{Timeout: 10 * time.Second}

// safeTransport: DialContext mencegah DNS rebinding — resolve manual, validasi,
// lalu dial IP hasil validasi langsung.
var safeTransport = &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := resolveBlocked(host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := safeDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	},
}

var dlClientSafe = &http.Client{Timeout: 20 * time.Second, Transport: safeTransport}

// dlGetSafe = dlGet + proteksi SSRF. Cek status code 2xx juga (dlGet tidak).
func dlGetSafe(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")
	resp, err := dlClientSafe.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, 2<<20) // cap 2MB
}

// dlGetSafeLimit = dlGetSafe dengan batas byte kustom — buat media yang lebih
// besar dari 2MB (video dari API pihak ketiga) tapi tetap anti-SSRF.
func dlGetSafeLimit(rawURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")
	resp, err := dlClientSafe.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, maxBytes)
}

func dlGet(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Fix: cek status HTTP — 4xx/5xx jangan dianggap response valid.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	// Fix: batasi ukuran response (100MB) — media raksasa tidak boleh
	// di-buffer penuh ke RAM tanpa batas.
	return readAllLimit(resp.Body, 100<<20)
}

func dlPost(endpoint string, body io.Reader, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest("POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Fix: cek status HTTP — 4xx/5xx jangan dianggap response valid.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, 100<<20)
}

// siputzxGet — GET endpoint api.siputzx.my.id dengan retry. Node siputzx
// sering balas 503 "All nodes failed" sesaat (flaky) tapi 200 pada percobaan
// berikutnya — 3 percobaan, jeda 2 detik.
func siputzxGet(path string) ([]byte, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		body, err := dlGet("https://api.siputzx.my.id"+path, nil)
		if err == nil {
			return body, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("siputzx: %w", lastErr)
}

// sendVideo upload & kirim video ke chat
func sendVideo(ctx context.Context, chat types.JID, data []byte, caption, mime string) error {
	if mime == "" {
		mime = "video/mp4"
	}
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return fmt.Errorf("upload video: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String(mime),
			Caption:       proto.String(caption),
			ContextInfo:   mergeReplyCtx(ctx, newsletterCtxInfo(ctx)),
		},
	})
	return err
}

// sendAudio upload & kirim audio ke chat
func sendAudio(ctx context.Context, chat types.JID, data []byte, mime string) error {
	if mime == "" {
		mime = "audio/mpeg"
	}
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return fmt.Errorf("upload audio: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String(mime),
			PTT:           proto.Bool(false),
			ContextInfo:   mergeReplyCtx(ctx, newsletterCtxInfo(ctx)),
		},
	})
	return err
}

// sendImage upload & kirim gambar ke chat
func sendImage(ctx context.Context, chat types.JID, data []byte, caption string) error {
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String("image/jpeg"),
			Caption:       proto.String(caption),
			ContextInfo:   mergeReplyCtx(ctx, newsletterCtxInfo(ctx)),
		},
	})
	return err
}

// ─── TikTok ──────────────────────────────────────────────────────────────────
// API: www.tikwm.com/api (primary, cepat) → fallback api.siputzx.my.id
// /api/d/tiktok/v2 kalau TikWM gagal. TikWM kadang balas 403 tanpa UA mobile,
// makanya dikirim header UA Android + timeout 20s di dlClient (shared).
// Cmd: !tt, !tiktok, !ttdl

// tikwmResp — https://www.tikwm.com/api/?url=<link> (tanpa hd=1: versi HD
// TikTok sering HEVC yang tidak bisa diputar WA Android — versi standar H.264)
type tikwmResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Title    string   `json:"title"`
		Duration int      `json:"duration"`
		Play     string   `json:"play"`   // no-watermark (standar, H.264)
		WMPlay   string   `json:"wmplay"` // with watermark, fallback kalau play kosong
		Images   []string `json:"images"`
		Author   struct {
			UniqueID string `json:"unique_id"`
			Nickname string `json:"nickname"`
		} `json:"author"`
	} `json:"data"`
}

// tikwmClient — client khusus TikWM dengan timeout lebih pendek (20s) dari
// dlClient global (60s) supaya !tt tidak menggantung kalau TikWM buntu.
var tikwmClient = &http.Client{Timeout: 20 * time.Second}

// tikwmFetch GET /api dengan retry singkat (TikWM sesekali timeout/node sibuk).
// hd=true menambah &hd=1 (versi HD) — dipakai command !tthd.
func tikwmFetch(query string, hd bool) (*tikwmResp, error) {
	// Tanpa hd=1: versi HD TikTok sering HEVC (gagal diputar WA Android),
	// versi standar H.264 dijamin playable — kualitas tetap no-watermark.
	base := "https://www.tikwm.com/api/?url=%s"
	if hd {
		base += "&hd=1"
	}
	var lastErr error
	for i := 0; i < 2; i++ {
		body, err := tikwmGet(fmt.Sprintf(base, url.QueryEscape(query)))
		if err != nil {
			lastErr = err
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		var res tikwmResp
		if err := json.Unmarshal(body, &res); err != nil {
			lastErr = err
			continue
		}
		return &res, nil
	}
	return nil, fmt.Errorf("tikwm: %w", lastErr)
}

func tikwmGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")
	req.Header.Set("Referer", "https://www.tiktok.com/")
	resp, err := tikwmClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, 100<<20)
}

type siputzxTikTokResp struct {
	Status bool `json:"status"`
	Data   struct {
		Text              string   `json:"text"`
		NoWatermarkLink   string   `json:"no_watermark_link"`
		NoWatermarkLinkHD string   `json:"no_watermark_link_hd"`
		MusicLink         string   `json:"music_link"`
		CoverLink         string   `json:"cover_link"`
		AuthorNickname    string   `json:"author_nickname"`
		Duration          string   `json:"duration"`
		Images            []string `json:"images"`
	} `json:"data"`
}

var reTikTokURL = regexp.MustCompile(`https://(vt|vm|www)\.tiktok\.com/[^\s]+`)

func handleTikTok(ctx context.Context, evt *events.Message, args string) {
	handleTikTokFetch(ctx, evt, args, false)
}

// handleTikTokHD — !tthd: sama seperti !tt tapi minta versi HD (hd=1).
func handleTikTokHD(ctx context.Context, evt *events.Message, args string) {
	handleTikTokFetch(ctx, evt, args, true)
}

func handleTikTokFetch(ctx context.Context, evt *events.Message, args string, hd bool) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Kirim link TikTok atau kata kunci.\n"+
				"Contoh: `%stt https://vm.tiktok.com/xxx`\n"+
				"Atau: `%stt nama lagu tiktok`", Prefix, Prefix))
		return
	}

	reactMsg(ctx, evt, "⏳")

	// Cari by keyword tidak didukung TikWM/siputzx — minta link langsung
	// biar tidak berakhir di error yang menyesatkan.
	if !reTikTokURL.MatchString(query) {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Cari by keyword tidak didukung — kirim link TikTok langsung ya.")
		return
	}

	// 1) Primary: TikWM (cepat, no-watermark). Gagal? lanjut fallback siputzx.
	if tr, err := tikwmFetch(query, hd); err == nil && tr.Code == 0 &&
		(tr.Data.Play != "" || len(tr.Data.Images) > 0) {
		title := tr.Data.Title
		if title == "" {
			title = "TikTok"
		}
		dur := ""
		if tr.Data.Duration > 0 {
			dur = fmt.Sprintf("%d detik", tr.Data.Duration)
		}
		author := tr.Data.Author.Nickname
		if tr.Data.Author.UniqueID != "" {
			author = "@" + tr.Data.Author.UniqueID
			if tr.Data.Author.Nickname != "" {
				author += " (" + tr.Data.Author.Nickname + ")"
			}
		}
		// Caption media = plain text di WhatsApp (markdown hanya di pesan teks)
		caption := fmt.Sprintf("🎵 %s\n👤 %s\n⏱ %s", title, author, dur)
		sendTikTokMedia(ctx, evt, chat, caption, tr.Data.Images, tr.Data.Play, tr.Data.WMPlay)
		return
	}

	// 2) Fallback: siputzx (lambat, sesekali 503 "All nodes failed").
	body, err := dlGet("https://api.siputzx.my.id/api/d/tiktok/v2?url="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch TikTok: "+err.Error())
		return
	}

	var res siputzxTikTokResp
	if err := json.Unmarshal(body, &res); err != nil || !res.Status {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ TikTok sedang tidak tersedia, coba lagi nanti.")
		return
	}

	d := res.Data
	title := d.Text
	if title == "" {
		title = "TikTok"
	}
	caption := fmt.Sprintf("🎵 %s\n👤 %s\n⏱ %s",
		title, d.AuthorNickname, d.Duration)

	sendTikTokMedia(ctx, evt, chat, caption, d.Images, d.NoWatermarkLinkHD, d.NoWatermarkLink)
}

// mp4Playable cek file MP4 tidak terpotong: parse box top-level, pastikan
// semua box lengkap sampai akhir. Download dari CDN TikTok (lintas benua)
// sering putus di tengah — file terpotong tetap HTTP 200 tapi tidak bisa
// diputar di WhatsApp.
//
// CATATAN: CDN TikTok menaruh trailing garbage di akhir file (pola box
// size<8 setelah mdat) — itu NORMAL dan harus diterima; yang ditolak hanya
// box yang ukurannya melampaui akhir file (terpotong).
func mp4Playable(data []byte) bool {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	off := 0
	sawMoov := false
	for off+8 <= len(data) {
		size := binary.BigEndian.Uint32(data[off : off+4])
		switch {
		case size == 1: // box 64-bit
			if off+16 > len(data) {
				return false
			}
			size64 := binary.BigEndian.Uint64(data[off+8 : off+16])
			if size64 < 16 || off+int(size64) > len(data) {
				return false // terpotong
			}
			off += int(size64)
		case size == 0: // box memanjang sampai akhir file
			return sawMoov
		case size < 8: // trailing garbage khas CDN TikTok — file sudah lengkap
			return sawMoov
		default:
			if off+int(size) > len(data) {
				return false // terpotong
			}
			if string(data[off+4:off+8]) == "moov" {
				sawMoov = true
			}
			off += int(size)
		}
	}
	return off == len(data) && sawMoov
}

// sendTikTokMedia kirim media TikTok — photo post (banyak gambar) atau video —
// lalu pasang reaksi ✅/❌. Nama fungsi mencerminkan apa yang dilakukannya.
func sendTikTokMedia(ctx context.Context, evt *events.Message, chat types.JID, caption string, images []string, videoURL, videoURL2 string) bool {
	// Photo post
	if len(images) > 0 {
		sendText(ctx, chat, fmt.Sprintf("📸 TikTok Photo (%d gambar)\n%s", len(images), caption))
		sent := 0
		for i, imgURL := range images {
			imgData, err := dlGet(imgURL, nil)
			if err != nil {
				pool.logger.Warn().Err(err).Msg("tiktok photo: download gambar gagal")
				continue
			}
			if err := sendImage(ctx, chat, imgData, fmt.Sprintf("%d/%d", i+1, len(images))); err != nil {
				pool.logger.Warn().Err(err).Msg("tiktok photo: kirim gambar gagal")
				continue
			}
			sent++
			time.Sleep(1 * time.Second)
		}
		if sent == 0 {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal download semua gambar.")
			return false
		}
		reactMsg(ctx, evt, "✅")
		return true
	}

	// Video post
	if videoURL == "" {
		videoURL = videoURL2
	}
	if videoURL == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video tidak bisa didownload.")
		return false
	}
	sendText(ctx, chat, "⏳ Mengunduh video...")
	vidData, err := dlGet(videoURL, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download video: "+err.Error())
		return false
	}
	// Download dari CDN TikTok (tiktokcdn-us.com, di Amerika) sering terpotong
	// dari server Indonesia — file rusak tetap HTTP 200. Kalau terdeteksi
	// terpotong, coba URL cadangan (wmplay, CDN tiktokv.us yang berbeda).
	if !mp4Playable(vidData) {
		pool.logger.Warn().Int("bytes", len(vidData)).Msg("tt: video play terpotong, coba wmplay")
		if videoURL2 != "" {
			if alt, err := dlGet(videoURL2, nil); err == nil && mp4Playable(alt) {
				vidData = alt
				pool.logger.Info().Int("bytes", len(alt)).Msg("tt: wmplay dipakai (play rusak)")
			} else {
				pool.logger.Warn().Msg("tt: wmplay juga gagal, kirim play apa adanya")
			}
		}
	}
	if err := sendVideo(ctx, chat, vidData, caption, "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim video: "+err.Error())
		return false
	}
	reactMsg(ctx, evt, "✅")
	return true
}

// ─── YouTube MP3 ──────────────────────────────────────────────────────────────
// API: api.siputzx.my.id /api/d/ummy (pengganti cnv.cx yang kena 403)
// Cmd: !ytmp3, !yta

var reYTID = regexp.MustCompile(`(?:youtu\.be/|watch\?v=|/shorts/|/live/|/embed/)([a-zA-Z0-9_-]{11})`)

func extractYTID(input string) string {
	m := reYTID.FindStringSubmatch(input)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// ummyResp — response https://api.siputzx.my.id/api/d/ummy?url=<yt>.
// Catatan: url[] berisi juga entri converter UI (du.sf-converter.com,
// isConverterUI=true) yang cuma halaman convert, bukan file langsung.
type ummyResp struct {
	Status bool `json:"status"`
	Data   struct {
		URL []struct {
			URL           string `json:"url"`
			Ext           string `json:"ext"`
			Quality       string `json:"quality"`
			Downloadable  bool   `json:"downloadable"`
			IsBundle      bool   `json:"isBundle"`
			IsConverterUI bool   `json:"isConverterUI"`
		} `json:"url"`
	} `json:"data"`
}

// ytFetchUmmy fetch data /api/d/ummy buat satu video YouTube (siputzx).
func ytFetchUmmy(videoID string) (*ummyResp, error) {
	ytURL := "https://www.youtube.com/watch?v=" + videoID
	body, err := dlGet("https://api.siputzx.my.id/api/d/ummy?url="+url.QueryEscape(ytURL), nil)
	if err != nil {
		return nil, err
	}
	var res ummyResp
	if err := json.Unmarshal(body, &res); err != nil || !res.Status {
		return nil, fmt.Errorf("YouTube sedang tidak tersedia, coba lagi nanti")
	}
	return &res, nil
}

// ytFetchMP3URL ambil URL audio via siputzx ummy (fallback kalau yt-dlp gagal).
// PENTING: entri "mp3" pertama biasanya isConverterUI (du.sf-converter.com) —
// itu halaman HTML converter, BUKAN file audio; didownload malah nyangkut di
// ffmpeg ("Invalid data found when processing input"). Skip isConverterUI dan
// prioritaskan audio asli dari googlevideo (m4a/opus) yang bisa langsung
// diproses ffmpeg (format auto-detect, ekstensi tidak penting).
func ytFetchMP3URL(videoID string) (string, error) {
	res, err := ytFetchUmmy(videoID)
	if err != nil {
		return "", err
	}
	var fallback string
	for _, u := range res.Data.URL {
		if u.IsConverterUI || u.URL == "" {
			continue
		}
		if u.Ext == "m4a" || u.Ext == "opus" {
			return u.URL, nil
		}
		if u.Ext == "mp3" && fallback == "" {
			fallback = u.URL
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("format audio tidak ditemukan untuk video ini")
}

// ytFetchMP4URL ambil URL mp4 via siputzx ummy — fallback kalau yt-dlp gagal.
// PENTING: jangan ambil mp4 pertama di url[] — 2 entri pertama itu converter UI
// (du.sf-converter.com, isConverterUI) yang cuma halaman convert, bukan file.
// Prioritas: format BUNDLE yang langsung diputar WA (itag 18 — 360p,
// avc1+mp4a, isBundle+downloadable), lalu mp4 downloadable lain.
func ytFetchMP4URL(videoID string) (string, error) {
	res, err := ytFetchUmmy(videoID)
	if err != nil {
		return "", err
	}
	var fallback string
	for _, u := range res.Data.URL {
		if u.Ext != "mp4" || u.URL == "" {
			continue
		}
		if u.IsBundle && u.Downloadable {
			return u.URL, nil
		}
		if fallback == "" && u.Downloadable {
			fallback = u.URL
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("format mp4 tidak ditemukan untuk video ini")
}

func handleYTMP3(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sytmp3 https://youtu.be/xxx`\nAtau: `%sytmp3 nama lagu`", Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	videoID := extractYTID(query)
	if videoID == "" {
		// Coba search via yt-dlp kalau tersedia
		if ytdlpAvailable() {
			ytURL, err := ytdlpSearch(query)
			if err != nil {
				reactMsg(ctx, evt, "❌")
				sendText(ctx, chat, "❌ Video tidak ditemukan: "+err.Error())
				return
			}
			videoID = extractYTID(ytURL)
		}
		if videoID == "" {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Masukkan link YouTube yang valid atau nama lagu.")
			return
		}
	}

	sendText(ctx, chat, "⏳ Mengunduh audio dari YouTube...")

	// Coba yt-dlp dulu kalau tersedia (lebih reliable)
	if ytdlpAvailable() {
		ytURL := "https://www.youtube.com/watch?v=" + videoID
		mp3Path, err := ytdlpDownloadAudio(ytURL)
		if err == nil {
			defer os.Remove(mp3Path)
			data, err := os.ReadFile(mp3Path)
			if err == nil {
				// Fix: kalau DOWNLOAD sukses tapi KIRIM gagal, jangan fallback
				// ke API fallback — itu download ulang penuh (bandwidth 2x) dan bisa
				// terkirim 2x kalau error-nya transien. Langsung lapor error.
				if err := sendAudio(ctx, chat, data, "audio/mpeg"); err == nil {
					reactMsg(ctx, evt, "✅")
					return
				} else {
					reactMsg(ctx, evt, "❌")
					sendText(ctx, chat, "❌ Gagal kirim audio: "+err.Error())
					return
				}
			}
		}
		pool.logger.Warn().Err(err).Msg("ytmp3: yt-dlp gagal, coba siputzx")
	}

	// Fallback: siputzx ummy API
	mp3URL, err := ytFetchMP3URL(videoID)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	data, err := dlGet(mp3URL, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download: "+err.Error())
		return
	}
	if err := sendAudio(ctx, chat, data, "audio/mpeg"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── YouTube Play (video + audio) ────────────────────────────────────────────
// Cmd: !ytplay <judul> — cari video YouTube by judul, kirim VIDEO MP4 + AUDIO
// MP3 ke chat. Beda dari .playvideo (yang video call).

func handleYTPlay(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sytplay another love`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	results, err := youtubeSearchSiputzx(query)
	if err != nil {
		// Fallback ke scrape HTML (regex lama).
		results, err = youtubeSearchHTML(query)
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mencari YouTube: "+err.Error())
		return
	}
	if len(results) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video tidak ditemukan.")
		return
	}
	videoID := results[0].id
	title := results[0].title
	if title == "" {
		title = query
	}
	ytURL := "https://www.youtube.com/watch?v=" + videoID
	sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh video + audio: *%s*...", title))

	// Audio & video di-download PARALEL (sebelumnya berurutan → 2× lebih
	// lambat). Masing-masing: yt-dlp primary → siputzx ummy fallback.
	type mediaResult struct {
		data []byte
	}
	var wg sync.WaitGroup
	var audio, video mediaResult
	wg.Add(2)

	go func() {
		defer wg.Done()
		if ytdlpAvailable() {
			if p, err := ytdlpDownloadAudio(ytURL); err == nil {
				audio.data, err = os.ReadFile(p)
				os.Remove(p)
				if err == nil {
					return
				}
				audio.data = nil
				pool.logger.Warn().Err(err).Msg("ytplay: baca file audio gagal")
			} else {
				pool.logger.Warn().Err(err).Msg("ytplay: yt-dlp audio gagal, coba siputzx")
			}
		}
		if mp3URL, perr := ytFetchMP3URL(videoID); perr == nil {
			audio.data, _ = dlGet(mp3URL, nil)
		}
	}()

	go func() {
		defer wg.Done()
		if ytdlpAvailable() {
			if p, err := ytdlpDownloadVideo(ytURL); err == nil {
				video.data, err = os.ReadFile(p)
				os.Remove(p)
				if err == nil {
					return
				}
				video.data = nil
				pool.logger.Warn().Err(err).Msg("ytplay: baca file video gagal")
			} else {
				pool.logger.Warn().Err(err).Msg("ytplay: yt-dlp video gagal, coba siputzx")
			}
		}
		if mp4URL, perr := ytFetchMP4URL(videoID); perr == nil {
			video.data, _ = dlGet(mp4URL, nil)
		}
	}()

	wg.Wait()

	if len(video.data) == 0 && len(audio.data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengunduh video & audio.")
		return
	}

	if len(video.data) > 0 {
		if err := sendVideo(ctx, chat, video.data, title, "video/mp4"); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal kirim video: "+err.Error())
		}
	}
	if len(audio.data) > 0 {
		if err := sendAudio(ctx, chat, audio.data, "audio/mpeg"); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal kirim audio: "+err.Error())
		}
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Twitter/X ────────────────────────────────────────────────────────────────
// API: savetwitter.net — Port dari downloader-twitter.js
// Cmd: !tw, !twitter, !xdl

var reTwitterMP4 = regexp.MustCompile(`href="(https://dl\.snapcdn\.app/get\?token=[^"]+)"[^>]*>MP4 \(([^)]+)\)`)

func handleTwitter(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	tweetURL := strings.TrimSpace(args)
	if tweetURL == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%stw https://x.com/user/status/xxx`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	form := url.Values{
		"q":       {tweetURL},
		"lang":    {"id"},
		"cftoken": {""},
	}
	body, err := dlPost("https://savetwitter.net/api/ajaxSearch",
		strings.NewReader(form.Encode()),
		map[string]string{
			"Content-Type":     "application/x-www-form-urlencoded",
			"X-Requested-With": "XMLHttpRequest",
			"Origin":           "https://savetwitter.net",
			"Referer":          "https://savetwitter.net/id3",
		})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}

	var res struct {
		Status int    `json:"status"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Status != 200 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video Twitter tidak ditemukan.")
		return
	}

	matches := reTwitterMP4.FindAllStringSubmatch(res.Data, -1)
	if len(matches) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak ada video MP4 ditemukan.")
		return
	}

	// Ambil kualitas terbaik (terakhir biasanya paling tinggi)
	best := matches[len(matches)-1]
	dlURL, quality := best[1], best[2]

	sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh Twitter/X video (%s)...", quality))
	vidData, err := dlGet(dlURL, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download: "+err.Error())
		return
	}
	caption := fmt.Sprintf("🐦 Twitter/X  •  Kualitas: %s", quality)
	if err := sendVideo(ctx, chat, vidData, caption, "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim video: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Instagram ────────────────────────────────────────────────────────────────
// API: snapinsta.top — Port dari down-instagram.js
// ⚠️ Status: API mati (000) sejak 2026-08-14, tidak ada pengganti yang hidup
// (siputzx d/* 503, itzpire/vreden 000, faa tidak punya endpoint). Handler
// dibiarkan sebagai dead code; command tidak terpanggil dari menu.
// Cmd: !ig, !instagram, !igdl

var reIGLinks = regexp.MustCompile(`class="download-items__btn"[^>]*><a href="([^"]+)"`)

func handleInstagram(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	igURL := strings.TrimSpace(args)
	if igURL == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sig https://www.instagram.com/p/xxx/`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	formData := &bytes.Buffer{}
	formData.WriteString("url=" + url.QueryEscape(igURL) + "&action=post")

	body, err := dlPost("https://snapinsta.top/action.php",
		formData,
		map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Origin":       "https://snapinsta.top",
			"Referer":      "https://snapinsta.top/",
		})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}

	var res struct {
		Status int    `json:"status"`
		Data   string `json:"data"`
	}
	_ = json.Unmarshal(body, &res)

	htmlContent := res.Data
	if htmlContent == "" {
		htmlContent = string(body)
	}

	links := reIGLinks.FindAllStringSubmatch(htmlContent, -1)
	if len(links) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Media Instagram tidak ditemukan. Pastikan link benar dan akun tidak private.")
		return
	}

	sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh %d media...", len(links)))
	sent := 0
	for _, m := range links {
		if len(m) < 2 {
			continue
		}
		mediaURL := m[1]
		data, err := dlGet(mediaURL, nil)
		if err != nil {
			pool.logger.Warn().Err(err).Msg("instagram: download media gagal")
			continue
		}
		// Deteksi video via magic bytes MP4 (ftyp)
		isVideo := len(data) > 8 && string(data[4:8]) == "ftyp"
		var serr error
		if isVideo {
			serr = sendVideo(ctx, chat, data, "📸 Instagram", "video/mp4")
		} else {
			serr = sendImage(ctx, chat, data, "📸 Instagram")
		}
		if serr != nil {
			pool.logger.Warn().Err(serr).Msg("instagram: kirim media gagal")
			continue
		}
		sent++
		time.Sleep(500 * time.Millisecond)
	}
	if sent == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media.")
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Facebook ─────────────────────────────────────────────────────────────────
// API: fbdownloader.to — Port dari down-facebook.js
// Cmd: !fb, !facebook, !fbdl

var (
	reFBKExp   = regexp.MustCompile(`k_exp="(.*?)"`)
	reFBKToken = regexp.MustCompile(`k_token="(.*?)"`)
	reFBVideo  = regexp.MustCompile(`(?s)<td class="video-quality">(.*?)</td>.*?(?:href="(.*?)"|data-videourl="(.*?)")`)
)

func fbGetToken() (kExp, kToken string, err error) {
	body, err := dlGet("https://fbdownloader.to/id", map[string]string{
		"Referer": "https://fbdownloader.to/",
	})
	if err != nil {
		return "", "", err
	}
	html := string(body)
	mExp := reFBKExp.FindStringSubmatch(html)
	mTok := reFBKToken.FindStringSubmatch(html)
	if len(mExp) < 2 || len(mTok) < 2 {
		return "", "", fmt.Errorf("token tidak ditemukan di halaman fbdownloader")
	}
	return mExp[1], mTok[1], nil
}

func handleFacebook(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fbURL := strings.TrimSpace(args)
	if fbURL == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sfb https://www.facebook.com/watch?v=xxx`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	kExp, kToken, err := fbGetToken()
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal dapat token: "+err.Error())
		return
	}

	form := url.Values{
		"k_exp":   {kExp},
		"k_token": {kToken},
		"q":       {fbURL},
		"lang":    {"id"},
		"v":       {"v2"},
		"p":       {"home"},
		"W":       {""},
	}
	body, err := dlPost("https://fbdownloader.to/api/ajaxSearch",
		strings.NewReader(form.Encode()),
		map[string]string{
			"Content-Type":     "application/x-www-form-urlencoded; charset=UTF-8",
			"X-Requested-With": "XMLHttpRequest",
			"Origin":           "https://fbdownloader.to",
			"Referer":          "https://fbdownloader.to/id",
		})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}

	var res struct {
		Status int    `json:"status"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Status != 200 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video Facebook tidak ditemukan.")
		return
	}

	matches := reFBVideo.FindAllStringSubmatch(res.Data, -1)
	if len(matches) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak ada video ditemukan di link ini.")
		return
	}

	quality := matches[0][1]
	dlURL := matches[0][2]
	if dlURL == "" {
		dlURL = matches[0][3]
	}

	sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh Facebook video (%s)...", quality))
	vidData, err := dlGet(dlURL, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download: "+err.Error())
		return
	}
	caption := fmt.Sprintf("📘 Facebook  •  Kualitas: %s", quality)
	if err := sendVideo(ctx, chat, vidData, caption, "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim video: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── SoundCloud ───────────────────────────────────────────────────────────────
// API: host.optikl.ink/soundcloud — Port dari down-soundcloud.js
// Cmd: !soundcloud, !sc

type scTrack struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
}
type scSearchResp []struct {
	No     int    `json:"no"`
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
}
type scDownloadResp struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Audio  string `json:"audio"`
}

func scSearch(query string) ([]scTrack, error) {
	body, err := dlGet(
		"https://host.optikl.ink/soundcloud/search?query="+url.QueryEscape(query),
		map[string]string{"User-Agent": "Mozilla/5.0 Windows NT 10.0 Chrome/124.0.0.0"},
	)
	if err != nil {
		return nil, err
	}
	var res scSearchResp
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	var tracks []scTrack
	for _, r := range res {
		tracks = append(tracks, scTrack{Title: r.Title, Author: r.Author, URL: r.URL})
	}
	return tracks, nil
}

func scDownload(scURL string) (*scDownloadResp, error) {
	body, err := dlGet(
		"https://host.optikl.ink/soundcloud/download?url="+url.QueryEscape(scURL),
		map[string]string{"User-Agent": "Mozilla/5.0 Windows NT 10.0 Chrome/124.0.0.0"},
	)
	if err != nil {
		return nil, err
	}
	var res scDownloadResp
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	if res.Audio == "" {
		return nil, fmt.Errorf("audio URL kosong")
	}
	return &res, nil
}

func handleSoundCloud(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%ssc another love` atau `%ssc https://soundcloud.com/...`", Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Kalau sudah URL langsung download
	if strings.HasPrefix(query, "http") {
		res, err := scDownload(query)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh: *%s* - %s", res.Title, res.Author))
		data, err := dlGet(res.Audio, nil)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal download: "+err.Error())
			return
		}
		if err := sendAudio(ctx, chat, data, "audio/mpeg"); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
			return
		}
		reactMsg(ctx, evt, "✅")
		return
	}

	// Search
	tracks, err := scSearch(query)
	if err != nil || len(tracks) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Lagu tidak ditemukan di SoundCloud.")
		return
	}

	// Langsung download hasil pertama
	best := tracks[0]
	sendText(ctx, chat, fmt.Sprintf("🎵 Ditemukan: *%s* - %s\n⏳ Mengunduh...", best.Title, best.Author))

	res, err := scDownload(best.URL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	data, err := dlGet(res.Audio, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download: "+err.Error())
		return
	}
	if err := sendAudio(ctx, chat, data, "audio/mpeg"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}
