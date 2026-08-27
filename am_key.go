package main

// am_key.go — kelola API key Alight Motion (MTZ).
//
// Alur: kalau key kedaluwarsa & user pakai command AM (!am-send/!am-aktif),
// bot mengarahkan ke kontak penjualan untuk beli key aktif (10k/bulan).
// Setelah dapat key, cukup kirim:
//
//	!amkey <apikey>     (alias !am-key)
//
// Key baru tersimpan di database/am_key.json dan langsung dipakai bot —
// tanpa build ulang. Prioritas key: hasil !amkey > env MTZ_API_KEY > config.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/types/events"
)

const (
	// amSalesNumber = kontak pembelian API key aktif.
	amSalesNumber = "6283163950589"
	// amKeyPath = lokasi persist key hasil !amkey.
	amKeyPath = "database/am_key.json"
)

var amKeyMu sync.Mutex

// amRuntimeKey baca key yang disimpan via !amkey (kosong kalau belum ada).
func amRuntimeKey() string {
	amKeyMu.Lock()
	defer amKeyMu.Unlock()
	data, err := os.ReadFile(amKeyPath)
	if err != nil {
		return ""
	}
	var st struct {
		APIKey string `json:"apiKey"`
	}
	if json.Unmarshal(data, &st) != nil {
		return ""
	}
	return strings.TrimSpace(st.APIKey)
}

// amSaveRuntimeKey simpan key baru ke file (persist restart).
func amSaveRuntimeKey(key string) error {
	amKeyMu.Lock()
	defer amKeyMu.Unlock()
	if err := os.MkdirAll("database", 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]string{"apiKey": key}, "", "  ")
	return os.WriteFile(amKeyPath, data, 0o644)
}

// amActiveKey — key yang sedang aktif dipakai bot.
// Prioritas: hasil !amkey > env MTZ_API_KEY > const MTZAPIKey (config.go).
func amActiveKey() string {
	if k := amRuntimeKey(); k != "" {
		return k
	}
	return mtzAPIKey()
}

// maskKey sembunyikan sebagian besar isi key buat ditampilkan.
func maskKey(k string) string {
	if len(k) <= 10 {
		return "***"
	}
	return k[:6] + "…" + k[len(k)-4:]
}

// amPurchaseMsg — pesan pengarah beli key saat key kedaluwarsa/habis.
func amPurchaseMsg() string {
	return fmt.Sprintf(
		"⏳ *API Key Alight Motion sudah tidak aktif*\n\n"+
			"> Masa aktif API key habis, jadi fitur Alight Motion belum bisa dipakai.\n\n"+
			"💰 *Beli API key aktif:*\n"+
			"> Harga: *10k / bulan*\n"+
			"> WhatsApp: wa.me/%s\n\n"+
			"✅ Sudah dapat key? Tinggal kirim:\n"+
			"> `%samkey <apikey>`\n"+
			"> Bot otomatis menyimpan & memakai key baru.",
		amSalesNumber, Prefix)
}

// handleAMKey — !amkey <apikey>: simpan API key baru (langsung aktif).
func handleAMKey(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	key := strings.TrimSpace(args)
	if key == "" {
		current := amActiveKey()
		shown := "_(kosong)_"
		if current != "" {
			shown = "`" + maskKey(current) + "`"
		}
		sendText(ctx, chat, fmt.Sprintf(
			"🔑 *API Key Alight Motion*\n\n"+
				"> Key aktif: %s\n\n"+
				"*Format:*\n"+
				"> `%samkey <apikey>` — ganti/simpan key baru\n"+
				"> Key langsung aktif tanpa restart\n\n"+
				"💰 Beli key aktif (10k/bulan): wa.me/%s",
			shown, Prefix, amSalesNumber))
		return
	}
	if err := amSaveRuntimeKey(key); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal simpan key: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf(
		"🔑 *API Key tersimpan!*\n\n"+
			"> Key baru: `%s`\n"+
			"> Status: *aktif* — langsung dipakai tanpa restart\n\n"+
			"Coba lagi: `%sam-send <email>`",
		maskKey(key), Prefix))
}