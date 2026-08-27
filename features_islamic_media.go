package main

// features_islamic_media.go — Fitur Islami & media.
// Berisi: murrotal (scrape islamipedia.id, tanpa key), meme (meme-api.com),
// animeinfo (Jikan API / MyAnimeList — publik tanpa key).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada: dlGet, sendText, sendImage, sendAudio, reactMsg.
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Murrotal ─────────────────────────────────────────────────────────────────
// Audio murottal per surah — port dari islamic/murrotal.js (Ourin).
// Sumber: scrape https://islamipedia.id/murottal/ (tanpa cheerio → regex,
// pola sama seperti !quran). Audio dari cdn.equran.id (Misyari Rasyid Al-Afasi).
// Cmd: !murrotal <nama/nomor surah>, !murottal, !audioquran, !quraudio

// maxMurottalBytes — batas ukuran audio yang dikirim (50MB). Surah panjang
// (Al-Baqarah ~60MB) melewati batas upload WhatsApp yang nyaman — tolak
// dengan pesan jelas daripada gagal di tengah upload.
const maxMurottalBytes = 50 << 20

// reSurahItem — satu blok `.surah-item`:
// <div class="surah-item" data-audio="URL" data-title="Nama">...<h5>N. Nama</h5><p>Arti</p>
var reSurahItem = regexp.MustCompile(`<div class="surah-item" data-audio="([^"]+)" data-title="([^"]+)"[^>]*>\s*<div>\s*<h5[^>]*>(\d+)\.\s*([^<]+)</h5><p>([^<]*)</p>`)

type surahInfo struct {
	No    int
	Nama  string
	Arti  string
	Audio string
}

// parseSurahList — ekstrak daftar surah dari HTML islamipedia.
func parseSurahList(htmlContent string) []surahInfo {
	rows := reSurahItem.FindAllStringSubmatch(htmlContent, -1)
	out := make([]surahInfo, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		no, _ := strconv.Atoi(r[3])
		out = append(out, surahInfo{
			No:    no,
			Nama:  html.UnescapeString(r[2]),
			Arti:  html.UnescapeString(strings.TrimSpace(r[5])),
			Audio: r[1],
		})
	}
	return out
}

// normSurah — normalisasi kata kunci: lowercase + buang non-alnum
// (sama seperti plugin asli: `.replace(/[^a-z0-9]/g, "")`).
func normSurah(s string) string {
	return regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(s), "")
}

// findSurah — cari surah dari daftar: cocok nomor persis, lalu contains
// (urutan daftar = urutan nomor surah, jadi "al" kena Al-Fatihah dulu? tidak —
// contains "al" cocok Al-Fatihah tapi juga semua; ambil yang pertama cocok).
func findSurah(list []surahInfo, query string) *surahInfo {
	if n, err := strconv.Atoi(query); err == nil {
		for i := range list {
			if list[i].No == n {
				return &list[i]
			}
		}
	}
	q := normSurah(query)
	for i := range list {
		if q != "" && strings.Contains(normSurah(list[i].Nama), q) {
			return &list[i]
		}
	}
	return nil
}

func handleMurrotal(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("🎧 *MURROTAL*\n\n> Masukkan nama surah\n\n`Contoh: %smurrotal al fatihah`\n`Contoh: %smurrotal ar rahman`", Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	body, err := dlGet("https://islamipedia.id/murottal/", nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil daftar surah.")
		return
	}
	surah := findSurah(parseSurahList(string(body)), query)
	if surah == nil || surah.Audio == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Surah *%s* tidak ditemukan.", query))
		return
	}

	// Cek ukuran dulu (HEAD) — surah panjang (Al-Baqarah dll) > 50MB.
	sizeOK := true
	if resp, err := http.Head(surah.Audio); err == nil {
		if resp.StatusCode == http.StatusOK && resp.ContentLength > maxMurottalBytes {
			sizeOK = false
		}
		resp.Body.Close()
	}
	if !sizeOK {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Surah *%s* terlalu besar untuk dikirim via WA (~%dMB). Coba surah yang lebih pendek.", surah.Nama, maxMurottalBytes>>20))
		return
	}

	sendText(ctx, chat, fmt.Sprintf("🎧 *%d. %s* — %s\n⏳ Mengunduh audio...", surah.No, surah.Nama, surah.Arti))
	data, err := dlGet(surah.Audio, nil)
	if err != nil || len(data) > maxMurottalBytes {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengunduh audio surah.")
		return
	}
	if err := sendAudio(ctx, chat, data, "audio/mpeg"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim audio: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Meme ─────────────────────────────────────────────────────────────────────
// Random meme dari Reddit (r/memes) — port dari random/meme.js (Ourin).
// Versi Ourin pakai neoxr key (limit habis) → ganti meme-api.com (publik).
// Cmd: !meme, !randommeme

type memeAPIResp struct {
	PostLink  string `json:"postLink"`
	Subreddit string `json:"subreddit"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	NSFW      bool   `json:"nsfw"`
}

func handleMeme(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	reactMsg(ctx, evt, "⏳")

	// Jangan kirim meme NSFW; coba maksimal 3x ambil yang bersih.
	var data []byte
	var caption string
	for attempt := 0; attempt < 3; attempt++ {
		body, err := dlGet("https://meme-api.com/gimme", nil)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal mengambil meme.")
			return
		}
		var res memeAPIResp
		if json.Unmarshal(body, &res) != nil || res.URL == "" || res.NSFW {
			continue
		}
		img, err := dlGet(res.URL, nil)
		if err != nil {
			continue
		}
		data = img
		caption = fmt.Sprintf("%s\n\n— r/%s", res.Title, res.Subreddit)
		break
	}
	if data == nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil meme.")
		return
	}
	if err := sendImage(ctx, chat, data, caption); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Anime Info ───────────────────────────────────────────────────────────────
// Cari info anime via Jikan API (MyAnimeList) — publik tanpa key, sangat stabil.
// Cmd: !animeinfo <judul>, !animesearch, !anime

type jikanResp struct {
	Data []struct {
		Title    string `json:"title"`
		Type     string `json:"type"`
		Episodes int    `json:"episodes"`
		Status   string `json:"status"`
		Score    float64 `json:"score"`
		Year     int    `json:"year"`
		URL      string `json:"url"`
		Synopsis string `json:"synopsis"`
		Images   struct {
			JPG struct {
				ImageURL string `json:"image_url"`
			} `json:"jpg"`
		} `json:"images"`
	} `json:"data"`
}

func handleAnimeInfo(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sanimeinfo naruto`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	body, err := dlGet("https://api.jikan.moe/v4/anime?q="+url.QueryEscape(query)+"&limit=1&order_by=members", nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi API anime.")
		return
	}
	var res jikanResp
	if json.Unmarshal(body, &res) != nil || len(res.Data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Anime *%s* tidak ditemukan.", query))
		return
	}
	a := res.Data[0]

	synopsis := strings.TrimSpace(a.Synopsis)
	if len(synopsis) > 400 {
		synopsis = synopsis[:397] + "..."
	}
	if synopsis == "" {
		synopsis = "-"
	}

	// Dua varian: plain untuk caption media (markdown tidak dirender di
	// caption), rich untuk fallback sendText kalau gambar gagal.
	plain := fmt.Sprintf("🎬 %s\n\n", a.Title)
	plain += fmt.Sprintf("📺 Type: %s\n", a.Type)
	plain += fmt.Sprintf("🔢 Episode: %d\n", a.Episodes)
	plain += fmt.Sprintf("📅 Rilis: %d\n", a.Year)
	plain += fmt.Sprintf("⭐ Skor: %.2f\n", a.Score)
	plain += fmt.Sprintf("📌 Status: %s\n\n", a.Status)
	plain += fmt.Sprintf("📝 %s\n\n🔗 %s", synopsis, a.URL)
	rich := fmt.Sprintf("🎬 *%s*\n\n", a.Title)
	rich += fmt.Sprintf("📺 *Type:* %s\n", a.Type)
	rich += fmt.Sprintf("🔢 *Episode:* %d\n", a.Episodes)
	rich += fmt.Sprintf("📅 *Rilis:* %d\n", a.Year)
	rich += fmt.Sprintf("⭐ *Skor:* %.2f\n", a.Score)
	rich += fmt.Sprintf("📌 *Status:* %s\n\n", a.Status)
	rich += fmt.Sprintf("📝 %s\n\n🔗 %s", synopsis, a.URL)

	if a.Images.JPG.ImageURL != "" {
		img, err := dlGet(a.Images.JPG.ImageURL, nil)
		if err == nil {
			if serr := sendImage(ctx, chat, img, plain); serr == nil {
				reactMsg(ctx, evt, "✅")
				return
			}
		}
	}
	sendText(ctx, chat, rich)
	reactMsg(ctx, evt, "✅")
}
