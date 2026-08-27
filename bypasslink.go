package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

func handleBypassLink(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	url := strings.TrimSpace(args)

	if url == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"❌ *Contoh penggunaan:*\n*%sbypasslink* https://linkvertise.com/...", Prefix))
		return
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		sendText(ctx, chat, "❌ Masukkan URL yang valid (harus dimulai http:// atau https://)")
		return
	}

	reactMsg(ctx, evt, "⏳")

	apiURL := "https://bintangapi.my.id/api/tools/bypassurl2?url=" + neturl.QueryEscape(url)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal request ke API: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
		Data    struct {
			Result string `json:"result"`
			URL    string `json:"url"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Response API tidak valid.")
		return
	}
	if !result.Success {
		reactMsg(ctx, evt, "❌")
		msg := result.Message
		if msg == "" {
			msg = "Bypass gagal."
		}
		sendText(ctx, chat, "❌ "+msg)
		return
	}

	original := result.Data.URL
	if original == "" {
		original = url
	}
	hasil := result.Data.Result
	if hasil == "" {
		hasil = "-"
	}

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf(
		"✅ *BYPASS URL BERHASIL*\n\n"+
			"🔗 *URL Asli:*\n%s\n\n"+
			"📂 *Hasil Bypass:*\n%s\n\n"+
			"> Powered by *%s*",
		original, hasil, BotName,
	))
}
