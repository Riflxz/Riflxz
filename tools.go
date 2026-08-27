package main

// tools.go — plugin tools: translate, qr, ssweb, tomp3, toimg, togif, tourl
// Port dari Anya MD plugins ke Go (whatsmeow + ffmpeg local).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types/events"
)

// ─── Translate ────────────────────────────────────────────────────────────────
// API: Google Translate — Port dari tool-translate.js
// Cmd: !tr, !translate, !tran

func handleTranslate(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	args = strings.TrimSpace(args)
	if args == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Contoh:\n`%str en Halo semua` — terjemahkan ke Inggris\n"+
				"`%str id Hello world` — terjemahkan ke Indonesia\n"+
				"Atau reply teks + `%str <kode_bahasa>`",
			Prefix, Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Parse: arg pertama = kode bahasa, sisanya = teks
	parts := strings.SplitN(args, " ", 2)
	targetLang := "id"
	text := args
	if len(parts) == 2 && len(parts[0]) <= 5 && !strings.Contains(parts[0], " ") {
		targetLang = parts[0]
		text = parts[1]
	}

	// Kalau tidak ada teks tapi ada quoted
	if text == targetLang {
		ci := msgContextInfo(evt)
		if q := ci.GetQuotedMessage(); q != nil {
			qText := q.GetConversation()
			if qText == "" {
				qText = q.GetExtendedTextMessage().GetText()
			}
			if qText != "" {
				text = qText
			}
		}
	}

	apiURL := fmt.Sprintf(
		"https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=%s&dt=t&q=%s",
		url.QueryEscape(targetLang),
		url.QueryEscape(text),
	)
	body, err := dlGet(apiURL, map[string]string{
		"Referer": "https://translate.google.com/",
	})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal translate: "+err.Error())
		return
	}

	// Parse: [[["translated","original",null,null,10]],...]
	var result []interface{}
	if err := json.Unmarshal(body, &result); err != nil || len(result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Response tidak valid.")
		return
	}
	var translated strings.Builder
	if segments, ok := result[0].([]interface{}); ok {
		for _, seg := range segments {
			if pair, ok := seg.([]interface{}); ok && len(pair) > 0 {
				if t, ok := pair[0].(string); ok {
					translated.WriteString(t)
				}
			}
		}
	}
	detectedLang := ""
	if len(result) > 2 {
		if dl, ok := result[2].(string); ok {
			detectedLang = dl
		}
	}

	out := fmt.Sprintf("🌐 *Terjemahan*\n_%s → %s_\n\n%s", detectedLang, targetLang, translated.String())
	sendText(ctx, chat, out)
	reactMsg(ctx, evt, "✅")
}

// ─── QR Code ──────────────────────────────────────────────────────────────────
// API: quickchart.io — Port dari tools-qrcode.js
// Cmd: !qr, !qrcode

func handleQRCode(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sqr https://wa.me/628xxx`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	apiURL := "https://quickchart.io/qr?text=" + url.QueryEscape(text)
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat QR code.")
		return
	}
	if err := sendImage(ctx, chat, data, "📱 QR Code\n"+text); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim gambar: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Screenshot Web ──────────────────────────────────────────────────────────
// API: shinana-bentosnap.hf.space — Port dari tools-ssweb.js
// Cmd: !ss, !ssweb, !webss

func handleScreenshot(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	args = strings.TrimSpace(args)
	if args == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Contoh: `%sss https://google.com`\n"+
				"Device: desktop (default), mobile, tablet",
			Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Parse device option
	device := "desktop_fhd"
	parts := strings.Fields(args)
	siteURL := parts[0]
	for _, p := range parts[1:] {
		switch strings.ToLower(p) {
		case "mobile":
			device = "mobile"
		case "tablet":
			device = "tablet"
		case "desktop":
			device = "desktop_fhd"
		}
	}
	if !strings.HasPrefix(siteURL, "http") {
		siteURL = "https://" + siteURL
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"url":       siteURL,
		"device":    device,
		"dark_mode": false,
		"wait_ms":   1500,
	})
	body, err := dlPost("https://shinana-bentosnap.hf.space/api/screenshot",
		bytes.NewReader(payload),
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal screenshot: "+err.Error())
		return
	}

	// Parse URL dari response
	var res map[string]interface{}
	if err := json.Unmarshal(body, &res); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Response tidak valid.")
		return
	}
	imgURL := ""
	for _, key := range []string{"url", "image", "result", "output"} {
		if v, ok := res[key].(string); ok && v != "" {
			imgURL = v
			break
		}
	}
	if imgURL == "" {
		if data, ok := res["data"].(map[string]interface{}); ok {
			if v, ok := data["url"].(string); ok {
				imgURL = v
			}
		}
	}
	if imgURL == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak dapat URL screenshot.")
		return
	}

	// Fix: dlGetSafe — screenshot di-download via URL yang dikasih API eksternal;
	// dlGet biasa ikut request internal (SSRF). 2MB cukup buat screenshot.
	imgData, err := dlGetSafe(imgURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download screenshot: "+err.Error())
		return
	}
	caption := fmt.Sprintf("🖥️ Screenshot\n🔗 %s\n📱 %s", siteURL, device)
	if err := sendImage(ctx, chat, imgData, caption); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim gambar: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── ToMP3 ────────────────────────────────────────────────────────────────────
// Local ffmpeg — Port dari tools-tomp3.js
// Cmd: !tomp3, !mp3, !toaudio

func handleToMP3(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply video/audio dulu, lalu ketik `%stomp3`", Prefix))
		return
	}

	reactMsg(ctx, evt, "⏳")

	var rawBytes []byte
	var err error
	inputExt := ".mp4"

	if vid := quoted.GetVideoMessage(); vid != nil {
		rawBytes, err = waClient.Download(ctx, vid)
		inputExt = ".mp4"
	} else if aud := quoted.GetAudioMessage(); aud != nil {
		rawBytes, err = waClient.Download(ctx, aud)
		inputExt = ".ogg"
	} else {
		sendText(ctx, chat, "❌ Hanya mendukung video atau audio.")
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media: "+err.Error())
		return
	}

	uid := uuid.New().String()
	if err := os.MkdirAll("temp", 0o755); err != nil {
		sendText(ctx, chat, "❌ Gagal buat temp dir")
		return
	}
	inPath := filepath.Join("temp", "tomp3_in_"+uid+inputExt)
	outPath := filepath.Join("temp", "tomp3_out_"+uid+".mp3")
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
		sendText(ctx, chat, "❌ Gagal tulis file.")
		return
	}

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath, "-vn", "-ab", "128k", "-ar", "44100", outPath)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal convert: "+lastLines(stderr.String(), 3))
		return
	}

	mp3Data, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil.")
		return
	}
	if err := sendAudio(ctx, chat, mp3Data, "audio/mpeg"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── ToImg (sticker → PNG) ────────────────────────────────────────────────────
// Local ffmpeg — Port dari tools-toimg.js (pakai ffmpeg, bukan sharp)
// Cmd: !toimg, !stk2img

func handleToImg(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply sticker/gambar dulu, lalu ketik `%stoimg`", Prefix))
		return
	}

	stk := quoted.GetStickerMessage()
	if stk == nil {
		sendText(ctx, chat, "❌ Hanya mendukung sticker (WebP).")
		return
	}

	reactMsg(ctx, evt, "⏳")

	rawBytes, err := waClient.Download(ctx, stk)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download sticker: "+err.Error())
		return
	}

	uid := uuid.New().String()
	if err := os.MkdirAll("temp", 0o755); err != nil {
		sendText(ctx, chat, "❌ Gagal buat temp dir")
		return
	}
	inPath := filepath.Join("temp", "toimg_in_"+uid+".webp")
	outPath := filepath.Join("temp", "toimg_out_"+uid+".png")
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
		sendText(ctx, chat, "❌ Gagal tulis file.")
		return
	}

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath, outPath)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal convert: "+lastLines(stderr.String(), 3))
		return
	}

	imgData, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil.")
		return
	}
	if err := sendImage(ctx, chat, imgData, "🖼️ Sticker → Gambar"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── ToGIF (sticker WebP → MP4) ───────────────────────────────────────────────
// Local ffmpeg — Port dari tools-togif.js (pakai ffmpeg, bukan ezgif scraping)
// Cmd: !togif, !tomp4, !stk2mp4

func handleToGIF(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply sticker animasi dulu, lalu ketik `%stogif`", Prefix))
		return
	}

	stk := quoted.GetStickerMessage()
	if stk == nil {
		sendText(ctx, chat, "❌ Hanya mendukung sticker WebP animasi.")
		return
	}

	reactMsg(ctx, evt, "⏳")

	rawBytes, err := waClient.Download(ctx, stk)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download sticker: "+err.Error())
		return
	}

	uid := uuid.New().String()
	if err := os.MkdirAll("temp", 0o755); err != nil {
		sendText(ctx, chat, "❌ Gagal buat temp dir")
		return
	}
	inPath := filepath.Join("temp", "togif_in_"+uid+".webp")
	outPath := filepath.Join("temp", "togif_out_"+uid+".mp4")
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
		sendText(ctx, chat, "❌ Gagal tulis file.")
		return
	}

	// Convert WebP animasi ke MP4
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath,
		"-vf", "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=white",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
		outPath)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal convert: "+lastLines(stderr.String(), 3))
		return
	}

	vidData, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil.")
		return
	}
	if err := sendVideo(ctx, chat, vidData, "🎞️ Sticker → Video", "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── ToURL (upload media → dapat link) ────────────────────────────────────────
// API: pone.rs — Port dari tools-tourl.js
// Cmd: !tourl, !tolink, !upload

func handleToURL(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()

	var rawBytes []byte
	var err error
	filename := "file"
	mimeType := "application/octet-stream"

	m := unwrapMsg(quoted)
	if m == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply media apapun dulu, lalu ketik `%stourl`", Prefix))
		return
	}
	if img := m.GetImageMessage(); img != nil {
		rawBytes, err = waClient.Download(ctx, img)
		mimeType = "image/jpeg"
		filename = "image.jpg"
	} else if vid := m.GetVideoMessage(); vid != nil {
		rawBytes, err = waClient.Download(ctx, vid)
		mimeType = "video/mp4"
		filename = "video.mp4"
	} else if aud := m.GetAudioMessage(); aud != nil {
		rawBytes, err = waClient.Download(ctx, aud)
		mimeType = "audio/ogg"
		filename = "audio.ogg"
	} else if doc := m.GetDocumentMessage(); doc != nil {
		rawBytes, err = waClient.Download(ctx, doc)
		mimeType = doc.GetMimetype()
		filename = doc.GetFileName()
		if filename == "" {
			exts, _ := mime.ExtensionsByType(mimeType)
			if len(exts) > 0 {
				filename = "file" + exts[0]
			}
		}
	} else if stk := m.GetStickerMessage(); stk != nil {
		rawBytes, err = waClient.Download(ctx, stk)
		mimeType = "image/webp"
		filename = "sticker.webp"
	} else {
		sendText(ctx, chat, "❌ Tipe media tidak didukung.")
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "🕒")

	// Upload ke pone.rs
	var body bytes.Buffer
	boundary := uuid.New().String()
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(fmt.Sprintf(`Content-Disposition: form-data; name="files[]"; filename="%s"`+"\r\n", filename))
	body.WriteString("Content-Type: " + mimeType + "\r\n\r\n")
	body.Write(rawBytes)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, _ := http.NewRequest("POST", "https://pone.rs/upload.php", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("User-Agent", "Mozilla/5.0 Android Chrome/147.0.0.0")
	req.Header.Set("Origin", "https://pone.rs")
	req.Header.Set("Referer", "https://pone.rs/")

	resp, err := dlClient.Do(req)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var res struct {
		Success bool `json:"success"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || !res.Success || len(res.Files) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Upload gagal atau response tidak valid.")
		return
	}

	fileURL := strings.ReplaceAll(res.Files[0].URL, `\/`, `/`)
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ *File berhasil diupload!*\n\n🔗 %s", fileURL))
}
