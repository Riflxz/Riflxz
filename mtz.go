package main

// mtz.go — Aktivasi akun via magic link (API mtzhacie.my.id).
// Command (gratis untuk semua user):
//
//	!am-send <email>     → kirim magic link ke email
//	!am-aktif <email> <link> → aktivasi akun pakai magic link
//
// Endpoint:
//
//	POST /api/v1/send-link  {"email": "..."}
//	POST /api/v1/aktifasi   {"email": "...", "link": "..."}
//
// Header: X-API-Key + Content-Type: application/json

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"go.mau.fi/whatsmeow/types/events"
)

const (
	mtzBaseURL  = "https://mtzhacie.my.id/api/v1"
	mtzSendLink = "/send-link"
	mtzAktifasi = "/aktifasi"
)

// mtzAPIKey — API key dari config, bisa di-override lewat env MTZ_API_KEY.
func mtzAPIKey() string {
	if v := os.Getenv("MTZ_API_KEY"); v != "" {
		return v
	}
	return MTZAPIKey
}

// errAMKeyExpired — penanda API key MTZ kedaluwarsa/tidak valid.
var errAMKeyExpired = fmt.Errorf("API key Alight Motion kedaluwarsa atau tidak valid")

// amKeyExpiredBody — potongan respon yang menandakan key bermasalah.
var amKeyExpiredHints = []string{
	"expired", "kedaluwarsa", "invalid api key", "api key invalid",
	"unauthorized", "invalid key", "key tidak valid", "quota", "kuota habis",
	"saldo habis", "saldo tidak cukup", "api key tidak ditemukan",
}

// isAMKeyExpiredResponse — deteksi key mati dari status HTTP + isi body.
func isAMKeyExpiredResponse(status int, body string) bool {
	if status == 401 || status == 402 || status == 403 {
		return true
	}
	low := strings.ToLower(body)
	for _, h := range amKeyExpiredHints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

// mtzPost — POST JSON ke endpoint mtzhacie.
// Return (body, httpStatus, error). error == errAMKeyExpired kalau key mati.
func mtzPost(endpoint string, payload map[string]string) (string, int, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", mtzBaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", amActiveKey())

	resp, err := dlClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(resp.Body); err != nil {
		return "", resp.StatusCode, err
	}
	raw := strings.TrimSpace(out.String())
	if isAMKeyExpiredResponse(resp.StatusCode, raw) {
		return raw, resp.StatusCode, errAMKeyExpired
	}
	return raw, resp.StatusCode, nil
}

// fmtMTZResponse — rapikan response API ke teks WhatsApp.
// API bisa balas JSON ({"message"/"msg"/"error"/"status"/"data"}) atau teks polos.
func fmtMTZResponse(raw string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	for _, key := range []string{"message", "msg", "error", "detail", "status"} {
		if v, ok := parsed[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	// Kalau ada data terstruktur, tampilkan sebagai JSON rapi
	return raw
}

// ─── !am-send <email> ────────────────────────────────────────────────────────

func handleAMSend(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	email := strings.TrimSpace(args)
	if email == "" || !strings.Contains(email, "@") {
		sendText(ctx, chat, fmt.Sprintf(
			"📧 *Kirim Magic Link*\n\n"+
				"> Kirim link aktivasi ke email\n\n"+
				"*Format:*\n"+
				"> `%sam-send user@mail.com`",
			Prefix))
		return
	}
	reactMsg(ctx, evt, "📧")
	resp, _, err := mtzPost(mtzSendLink, map[string]string{"email": email})
	if err == errAMKeyExpired {
		reactMsg(ctx, evt, "⏳")
		sendText(ctx, chat, amPurchaseMsg())
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim link: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "📧 *Kirim Magic Link*\n\n> Email: `"+email+"`\n\n"+fmtMTZResponse(resp))
}

// ─── !am-aktif <email> <link> ────────────────────────────────────────────────

func handleAMAktif(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"🔑 *Aktivasi Akun*\n\n"+
				"> Aktivasi akun pakai magic link dari email\n\n"+
				"*Format:*\n"+
				"> `%sam-aktif user@mail.com https://...link...`",
			Prefix))
		return
	}
	email := fields[0]
	link := fields[1]
	if !strings.Contains(email, "@") {
		sendText(ctx, chat, "❌ Email tidak valid: `"+email+"`")
		return
	}
	reactMsg(ctx, evt, "🔑")
	resp, _, err := mtzPost(mtzAktifasi, map[string]string{
		"email": email,
		"link":  link,
	})
	if err == errAMKeyExpired {
		reactMsg(ctx, evt, "⏳")
		sendText(ctx, chat, amPurchaseMsg())
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal aktivasi: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔑 *Aktivasi Akun*\n\n> Email: `"+email+"`\n\n"+fmtMTZResponse(resp))
}
