package main

// reactchannel.go — WhatsApp Channel Reactor (port dari JS Omegatech ke Go).
//
// Fitur: me-react ke sebuah post di channel WhatsApp lewat API pihak ketiga.
// Alur: ambil token recaptcha → tukar jadi temp API key → kirim reaksi.
//
// ⚠️ JWT dipakai dari config.go (ReactChannelJWT) atau env RCH_JWT — BUKAN
// di-hardcode di sini. JWT punya masa berlaku (exp), jadi kalau sudah kedaluwarsa
// tinggal ganti di config/env tanpa ubah kode.
//
// 🙏 Bot ini TIDAK BERAFILIASI dengan API Omegatech/back.asitha.top — cuma
// memakai endpoint-nya.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

const (
	rchSiteKey    = "6LemKk8sAAAAAH5PB3f1EspbMlXjtwv5C8tiMHSm"
	rchBackendURL = "https://back.asitha.top/api"
	rchCaptchaURL = "https://omegatech-api.dixonomega.tech/api/tools/recaptcha-v3"
)

// reactChannelJWT: pakai env RCH_JWT kalau ada (buat deploy pakai secret
// manager), kalau nggak pakai ReactChannelJWT dari config.go.
func reactChannelJWT() string {
	if k := os.Getenv("RCH_JWT"); k != "" {
		return k
	}
	return ReactChannelJWT
}

// reactChannelClient — HTTP client untuk API ReactChannel.
type reactChannelClient struct {
	jwt  string
	http *http.Client
}

func newReactChannelClient() *reactChannelClient {
	return &reactChannelClient{
		jwt:  reactChannelJWT(),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// getRecaptchaToken — minta token recaptcha-v3 dari endpoint bypass.
func (c *reactChannelClient) getRecaptchaToken() (string, error) {
	u := rchCaptchaURL + "?sitekey=" + url.QueryEscape(rchSiteKey) +
		"&url=" + url.QueryEscape(rchBackendURL) + "&use_enterprise=false"

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("recaptcha request gagal: %w", err)
	}
	defer resp.Body.Close()

	var parsed struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return "", fmt.Errorf("parse recaptcha response gagal: %w", err)
	}
	if !parsed.Success || parsed.Token == "" {
		return "", fmt.Errorf("recaptcha bypass gagal: %s", parsed.Message)
	}
	return parsed.Token, nil
}

// getTempAPIKey — tukar token recaptcha jadi temp API key.
func (c *reactChannelClient) getTempAPIKey(token string) (string, error) {
	body, _ := json.Marshal(map[string]string{"recaptcha_token": token})
	req, err := http.NewRequest(http.MethodPost, rchBackendURL+"/user/get-temp-token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.jwt)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("temp token request gagal: %w", err)
	}
	defer resp.Body.Close()

	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return "", fmt.Errorf("parse temp token response gagal: %w", err)
	}
	if parsed.Token == "" {
		return "", fmt.Errorf("temp API key gagal (cek JWT / status HTTP %d)", resp.StatusCode)
	}
	return parsed.Token, nil
}

// reactToPost — kirim reaksi ke post channel. `reacts` = emoji dipisah koma.
func (c *reactChannelClient) reactToPost(postLink, reacts string) error {
	token, err := c.getRecaptchaToken()
	if err != nil {
		return err
	}
	tempKey, err := c.getTempAPIKey(token)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]string{
		"post_link": postLink,
		"reacts":    reacts,
	})
	u := rchBackendURL + "/channel/react-to-post?apiKey=" + url.QueryEscape(tempKey)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.jwt)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("react request gagal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var parsed struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed)
		if parsed.Message != "" {
			return fmt.Errorf("API menolak (HTTP %d): %s", resp.StatusCode, parsed.Message)
		}
		return fmt.Errorf("API menolak (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// handleReactChannel — handler !rch / !reactch.
// Format: !rch <link channel> <emoji1,emoji2,...>
func handleReactChannel(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat

	if reactChannelJWT() == "" {
		sendText(ctx, chat, "❌ JWT ReactChannel belum diatur. Isi `ReactChannelJWT` di config.go atau env `RCH_JWT`.")
		return
	}

	fields := strings.Fields(args)
	if len(fields) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"⚡ *React Channel*\n\n"+
				"> Reaksi ke post channel WhatsApp\n\n"+
				"*Format:*\n"+
				"> `%srch <link> <emoji1,emoji2>`\n\n"+
				"*Contoh:*\n"+
				"> `%srch https://whatsapp.com/channel/xxx 😭,🔥`",
			Prefix, Prefix))
		return
	}

	postLink := fields[0]
	reactsRaw := strings.Join(fields[1:], " ")

	if !strings.Contains(postLink, "whatsapp.com/channel/") {
		sendText(ctx, chat, "❌ Link channel WhatsApp tidak valid.")
		return
	}

	var emojis []string
	for _, e := range strings.Split(reactsRaw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			emojis = append(emojis, e)
		}
	}
	if len(emojis) == 0 {
		sendText(ctx, chat, "❌ Emoji tidak boleh kosong.")
		return
	}
	if len(emojis) > 4 {
		sendText(ctx, chat, "❌ Maksimal 4 emoji.")
		return
	}

	reactMsg(ctx, evt, "🕒")

	client := newReactChannelClient()
	if err := client.reactToPost(postLink, strings.Join(emojis, ",")); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔥 Reaksi berhasil dikirim.")
}
