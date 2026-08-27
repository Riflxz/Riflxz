package main

// store_search.go — plugin port dari Anya MD-Update:
//   - codesnap  (maker-codesnap.js)     : screenshot kode → gambar
//   - playstore (search-playstore.js)   : cari aplikasi Play Store
//   - happymod  (search-happymod.js)    : cari APK mod HappyMod
//   - douyin    (downloader-douyin.js)  : download video Douyin
//
// Semua endpoint di sini FIXED (bukan input user), jadi dlGet biasa aman —
// proteksi SSRF (dlGetSafe) khusus URL arbitrer dari user (!fetch/!source).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── CodeSnap ─────────────────────────────────────────────────────────────────
// Cmd: !codesnap <kode>

func handleCodeSnap(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "codesnap const x = 42")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "✨")
	img, err := dlGet("https://api-faa.my.id/faa/codesnap?text="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal generate codesnap: "+err.Error())
		return
	}
	if err := sendImage(ctx, evt.Info.Chat, img, "✨ CodeSnap\n\n"+query); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal kirim gambar: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Play Store Search ────────────────────────────────────────────────────────
// Cmd: !playstore <query>

func handlePlayStore(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "playstore kinemaster")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "✨")
	body, err := dlGet("https://api.deline.my.id/search/playstore?q="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari Play Store: "+err.Error())
		return
	}
	var response struct {
		Status bool `json:"status"`
		Result []struct {
			Nama      string `json:"nama"`
			Developer string `json:"developer"`
			Rate2     string `json:"rate2"`
			Link      string `json:"link"`
			LinkDev   string `json:"link_dev"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil || !response.Status || len(response.Result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Aplikasi tidak ditemukan.")
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "📲 *PLAY STORE SEARCH*\nQuery: *%s*\n", query)
	for i, v := range response.Result[:min(5, len(response.Result))] {
		fmt.Fprintf(&out, "\n%d. *%s*\n", i+1, v.Nama)
		fmt.Fprintf(&out, "   👨‍💻 Developer: %s\n", v.Developer)
		fmt.Fprintf(&out, "   ⭐ Rating: %s\n", v.Rate2)
		fmt.Fprintf(&out, "   🔗 App: %s\n", v.Link)
		if v.LinkDev != "" {
			fmt.Fprintf(&out, "   🏢 Dev: %s\n", v.LinkDev)
		}
	}
	sendText(ctx, evt.Info.Chat, out.String())
	reactMsg(ctx, evt, "✅")
}

// ─── HappyMod Search ──────────────────────────────────────────────────────────
// Cmd: !happymod <query>

func handleHappyMod(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "happymod kinemaster")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "✨")
	body, err := dlGet("https://api.deline.my.id/search/happymod?q="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari HappyMod: "+err.Error())
		return
	}
	var response struct {
		Status bool `json:"status"`
		Result []struct {
			Title   string `json:"title"`
			Package string `json:"package"`
			Version string `json:"version"`
			Size    string `json:"size"`
			ModInfo string `json:"modInfo"`
			PageDL  string `json:"page_dl"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil || !response.Status || len(response.Result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Aplikasi tidak ditemukan.")
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "📱 *HAPPYMOD SEARCH*\nQuery: *%s*\n", query)
	for i, v := range response.Result[:min(5, len(response.Result))] {
		fmt.Fprintf(&out, "\n%d. *%s*\n", i+1, v.Title)
		fmt.Fprintf(&out, "   📦 Package: %s\n", v.Package)
		fmt.Fprintf(&out, "   🔢 Version: %s\n", v.Version)
		fmt.Fprintf(&out, "   📏 Size: %s\n", v.Size)
		if v.ModInfo != "" {
			fmt.Fprintf(&out, "   ✨ Mod: %s\n", v.ModInfo)
		}
		if v.PageDL != "" {
			fmt.Fprintf(&out, "   🔗 %s\n", v.PageDL)
		}
	}
	sendText(ctx, evt.Info.Chat, out.String())
	reactMsg(ctx, evt, "✅")
}

// ─── Douyin Downloader ────────────────────────────────────────────────────────
// Cmd: !douyin <url>

func handleDouyin(ctx context.Context, evt *events.Message, args string) {
	rawURL := strings.TrimSpace(args)
	if rawURL == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%sdouyin https://v.douyin.com/xxxxx`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	// API: api-faa.my.id (hidup per audit 2026-08-14; "Gagal memproses" hanya
	// jika URL tidak valid). Catatan: siputzx /api/d/* semuanya 503 — jangan
	// dipakai untuk download.
	body, err := dlGet("https://api-faa.my.id/faa/douyin-down?url="+url.QueryEscape(rawURL), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mengambil data Douyin: "+err.Error())
		return
	}
	var response struct {
		Status bool `json:"status"`
		Result struct {
			Title     string `json:"title"`
			Thumbnail string `json:"thumbnail"`
			Medias    []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"medias"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil || !response.Status || response.Result.Title == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mengambil data Douyin (link mungkin salah).")
		return
	}
	data := response.Result
	videoURL := ""
	for _, m := range data.Medias {
		if m.Type == "video" && m.URL != "" {
			videoURL = m.URL
			break
		}
	}
	if videoURL == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Video tidak ditemukan di hasil Douyin.")
		return
	}
	reactMsg(ctx, evt, "📥")
	// Caption media plain (markdown tidak dirender di caption WhatsApp)
	caption := fmt.Sprintf("✨ DOUYIN DOWNLOADER\n\n📌 Judul: %s", data.Title)
	// Fix: dlGetSafeLimit — URL video dari API eksternal (anti-SSRF), cap 100MB.
	vidData, err := dlGetSafeLimit(videoURL, 100<<20)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal download video: "+err.Error())
		return
	}
	if err := sendVideo(ctx, evt.Info.Chat, vidData, caption, "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal kirim video: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}
