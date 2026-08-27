package main

// sticker_maker.go — plugin Sticker & Maker
// Port dari Anya MD: tenor, smeme, brat, bratvid,
// maker-berita, faketweet, ytcomment, iqc, hitamkan, tomanga, qc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Helper: kirim sticker ────────────────────────────────────────────────────

func sendStickerBytes(ctx context.Context, chat types.JID, data []byte, mime string) error {
	// Kalau bukan webp, convert via ffmpeg dulu
	if !strings.Contains(mime, "webp") {
		uid := uuid.New().String()
		_ = os.MkdirAll("temp", 0o755)
		inPath := filepath.Join("temp", "stk_in_"+uid)
		outPath := filepath.Join("temp", "stk_out_"+uid+".webp")
		defer os.Remove(inPath)
		defer os.Remove(outPath)
		_ = os.WriteFile(inPath, data, 0o644)
		var stderr bytes.Buffer
		cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
			"-i", inPath,
			"-vf", "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000",
			"-c:v", "libwebp", "-lossless", "0", "-q:v", "70", outPath)
		cmd.Stderr = &stderr
		// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
		if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
			return fmt.Errorf("convert sticker: %s", lastLines(stderr.String(), 2))
		}
		converted, err := os.ReadFile(outPath)
		if err != nil {
			return err
		}
		data = converted
	}

	up, err := waClient.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload sticker: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String("image/webp"),
		},
	})
	return err
}

// getProfilePicURL ambil URL foto profil user
func getProfilePicURL(ctx context.Context, jid types.JID) string {
	info, err := waClient.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{})
	if err != nil || info == nil {
		return ""
	}
	return info.URL
}

// ─── Tenor Sticker ────────────────────────────────────────────────────────────
// API: faa stickerly — Port dari sticker-tenor.js
// Cmd: !tenor <query>

func handleTenor(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%stenor kucing lucu`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	body, err := dlGet("https://api-faa.my.id/faa/stickerly?q="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}

	var res struct {
		Status bool `json:"status"`
		Result []struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil || !res.Status || len(res.Result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Sticker tidak ditemukan.")
		return
	}

	sent := 0
	limit := 5
	if len(res.Result) < limit {
		limit = len(res.Result)
	}
	for _, r := range res.Result[:limit] {
		stkData, err := dlGet(r.URL, nil)
		if err != nil || len(stkData) == 0 {
			continue
		}
		if err := sendStickerBytes(ctx, chat, stkData, "image/webp"); err == nil {
			sent++
		}
	}
	if sent == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim sticker.")
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Brat Sticker ─────────────────────────────────────────────────────────────
// API: aqul-brat.hf.space — Port dari brat.js
// Cmd: !brat <teks>

func handleBrat(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		ci := msgContextInfo(evt)
		if q := ci.GetQuotedMessage(); q != nil {
			text = q.GetConversation()
			if text == "" {
				text = q.GetExtendedTextMessage().GetText()
			}
		}
	}
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sbrat teks kamu`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	// API ini return PNG — sendStickerBytes convert otomatis ke webp
	// (jangan di-pass "image/webp": data PNG yang dipaksa webp = sticker rusak).
	data, err := dlGet("https://aqul-brat.hf.space?text="+url.QueryEscape(text), nil)
	if err != nil || len(data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat brat sticker.")
		return
	}
	if err := sendStickerBytes(ctx, chat, data, "image/png"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Brat Video ───────────────────────────────────────────────────────────────
// API: brat.siputzx.my.id/gif — Port dari bratvid.js
// Cmd: !bratvid <teks>

func handleBratVid(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sbratvid teks kamu`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	data, err := dlGet("https://brat.siputzx.my.id/gif?text="+url.QueryEscape(text), nil)
	if err != nil || len(data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat brat gif.")
		return
	}
	// GIF → sticker animasi webp via ffmpeg
	uid := uuid.New().String()
	_ = os.MkdirAll("temp", 0o755)
	inPath := filepath.Join("temp", "bratvid_"+uid+".gif")
	outPath := filepath.Join("temp", "bratvid_"+uid+".webp")
	defer os.Remove(inPath)
	defer os.Remove(outPath)
	_ = os.WriteFile(inPath, data, 0o644)
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath,
		"-vf", "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000,fps=15",
		"-c:v", "libwebp_anim", "-lossless", "0", "-q:v", "70", "-loop", "0", outPath)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal convert: "+lastLines(stderr.String(), 2))
		return
	}
	webpData, _ := os.ReadFile(outPath)
	if err := sendStickerBytes(ctx, chat, webpData, "image/webp"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Smeme Sticker ────────────────────────────────────────────────────────────
// API: uguu.se + memegen.link — Port dari sticker-smeme.js
// Cmd: !smeme <atas|bawah>

func handleSmeme(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar + ketik `%ssmeme teks atas|teks bawah`", Prefix))
		return
	}
	parts := strings.SplitN(args, "|", 2)
	top, bottom := "_", "_"
	if len(parts) >= 1 && strings.TrimSpace(parts[0]) != "" {
		top = parts[0]
	}
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		bottom = parts[1]
	}

	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download gambar.")
		return
	}
	imgURL, err := uguuUpload(imgData, "image.jpg")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	memeURL := fmt.Sprintf("https://api.memegen.link/images/custom/%s/%s.png?background=%s",
		url.PathEscape(top), url.PathEscape(bottom), url.QueryEscape(imgURL))
	memeData, err := dlGet(memeURL, nil)
	if err != nil || len(memeData) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat meme.")
		return
	}
	if err := sendStickerBytes(ctx, chat, memeData, "image/png"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Maker Berita ─────────────────────────────────────────────────────────────
// API: api-nanzz.my.id — Port dari maker-berita.js
// Cmd: !berita <judul>|<url_gambar>

func handleBerita(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	var judul, imgURL string
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()

	if quoted != nil && quoted.GetImageMessage() != nil {
		imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
		if err == nil {
			imgURL, _ = uguuUpload(imgData, "image.jpg")
		}
		judul = strings.TrimSpace(args)
	} else {
		parts := strings.SplitN(args, "|", 2)
		if len(parts) < 2 {
			sendText(ctx, chat, fmt.Sprintf("❌ Contoh:\n`%sberita Judul Berita|https://url-gambar.jpg`\nAtau reply gambar + judul", Prefix))
			return
		}
		judul = strings.TrimSpace(parts[0])
		imgURL = strings.TrimSpace(parts[1])
	}
	if judul == "" || imgURL == "" {
		sendText(ctx, chat, "❌ Judul dan URL gambar wajib diisi.")
		return
	}

	reactMsg(ctx, evt, "⏳")
	apiURL := fmt.Sprintf("https://api-nanzz.my.id/docs/api/maker/berita.php?text=%s&url=%s",
		url.QueryEscape(judul), url.QueryEscape(imgURL))
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat berita card.")
		return
	}
	if err := sendImage(ctx, chat, data, "📰 "+judul); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Fake Tweet ───────────────────────────────────────────────────────────────
// API: deline.my.id — Port dari maker-faketweet.js
// ⚠️ Status: API mati (DNS 000) sejak 2026-08-14; siputzx /api/canvas/tweet 503
// semua node. Command sudah dihapus dari menu — handler dead code.
// Cmd: !faketweet <nama>|<username>|<teks>

func handleFakeTweet(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.SplitN(args, "|", 3)
	if len(parts) < 3 {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sfaketweet Nama|@username|teks tweet`", Prefix))
		return
	}
	name := strings.TrimSpace(parts[0])
	username := strings.TrimPrefix(strings.TrimSpace(parts[1]), "@")
	comment := strings.TrimSpace(parts[2])

	reactMsg(ctx, evt, "⏳")
	ppURL := getProfilePicURL(ctx, evt.Info.Sender.ToNonAD())
	apiURL := fmt.Sprintf("https://api.deline.my.id/maker/faketweet?name=%s&username=%s&comment=%s&avatar=%s&verified=false",
		url.QueryEscape(name), url.QueryEscape(username),
		url.QueryEscape(comment), url.QueryEscape(ppURL))
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat fake tweet.")
		return
	}
	if err := sendImage(ctx, chat, data, "🐦 Fake Tweet"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── YT Comment ───────────────────────────────────────────────────────────────
// API: deline.my.id — Port dari maker-ytcomment.js
// Cmd: !ytcomment <username>|<komentar>

func handleYTComment(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.SplitN(args, "|", 2)
	if len(parts) < 2 {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sytcomment Username|Komentar kamu`", Prefix))
		return
	}
	username := strings.TrimSpace(parts[0])
	text := strings.TrimSpace(parts[1])

	reactMsg(ctx, evt, "⏳")
	ppURL := getProfilePicURL(ctx, evt.Info.Sender.ToNonAD())
	apiURL := fmt.Sprintf("https://api.deline.my.id/maker/ytcomment?text=%s&username=%s&avatar=%s",
		url.QueryEscape(text), url.QueryEscape(username), url.QueryEscape(ppURL))
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat YT comment.")
		return
	}
	if err := sendImage(ctx, chat, data, "▶️ Fake YouTube Comment"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── iPhone Chat (IQC) ────────────────────────────────────────────────────────
// API: brat.siputzx.my.id — Port dari maker-iqc.js
// Cmd: !iqc <teks pesan>

func handleIQC(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		ci := msgContextInfo(evt)
		if q := ci.GetQuotedMessage(); q != nil {
			text = q.GetConversation()
			if text == "" {
				text = q.GetExtendedTextMessage().GetText()
			}
		}
	}
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%siqc Halo, apa kabar?`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	apiURL := fmt.Sprintf(
		"https://brat.siputzx.my.id/iphone-quoted?time=12.00&batteryPercentage=90&carrierName=AXIS&messageText=%s&emojiStyle=apple",
		url.QueryEscape(text))
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat iPhone chat.")
		return
	}
	if err := sendImage(ctx, chat, data, "📱 iPhone Chat"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Image Effect (Hitamkan, dll) ─────────────────────────────────────────────
// API: api-faa.my.id — Port dari maker-hitamkan.js
// Cmd: !tohitam, !tozombie, !toroblox, dll (50+ efek)

var faaEffects = map[string]string{
	"tohitam": "tohitam", "toputih": "toputih", "tozombie": "tozombie",
	"toroblox": "toroblox", "tomirror": "tomirror", "tochibi": "tochibi",
	"toghibli": "toghibli", "tojapanese": "tojapanese", "tojepang": "tojepang",
	"tolego": "tolego", "toreal": "toreal", "totua": "totua",
	"tomoai": "tomoai", "tomonyet": "tomonyet", "topacar": "topacar",
	"toroh": "toroh", "totato": "totato", "toviking": "toviking",
	"tobotak": "tobotak", "tofunk": "tofunk", "tofigura": "tofigura",
	"tohijab": "tohijab", "tokacamata": "tokacamata", "tokamboja": "tokamboja",
	"toliquor": "toliquor", "tomaid": "tomaid", "topeci": "topeci",
	"topiramida": "topiramida", "tounderground": "tounderground",
}

func handleFAAEffect(ctx context.Context, evt *events.Message, cmd string) {
	chat := evt.Info.Chat
	effect, ok := faaEffects[cmd]
	if !ok {
		effect = cmd
	}
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu, lalu ketik `%s%s`", Prefix, cmd))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download.")
		return
	}
	imgURL, err := uguuUpload(imgData, "image.jpg")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	apiURL := fmt.Sprintf("https://api-faa.my.id/faa/%s?url=%s", effect, url.QueryEscape(imgURL))
	data, err := dlGet(apiURL, nil)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal apply efek "+effect+".")
		return
	}
	if err := sendImage(ctx, chat, data, "✨ "+cmd); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Tomanga ──────────────────────────────────────────────────────────────────
// API: theresav.biz.id — Port dari image-tomanga.js
// Cmd: !tomanga, !manga

func handleToManga(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	if TheresavAPIKey == "" {
		sendText(ctx, chat, "❌ TheresavAPIKey belum diset di config.go")
		return
	}
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu, lalu ketik `%stomanga`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download.")
		return
	}

	var body bytes.Buffer
	boundary := uuid.New().String()
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="image"; filename="image.jpg"` + "\r\n")
	body.WriteString("Content-Type: image/jpeg\r\n\r\n")
	body.Write(imgData)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	apiURL := "https://api.theresav.biz.id/image/tomanga?apikey=" + TheresavAPIKey +
		"&style=" + url.QueryEscape("Ubah foto menjadi ilustrasi manga Jepang hitam putih")
	data, err := dlPost(apiURL, &body, map[string]string{
		"Content-Type": "multipart/form-data; boundary=" + boundary,
		"apikey":       TheresavAPIKey,
	})
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal convert ke manga.")
		return
	}
	if err := sendImage(ctx, chat, data, "🎌 Manga Style"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Quote Card ───────────────────────────────────────────────────────────────
// API: bot.lyo.su — Port dari qc.js
// Cmd: !qc <teks>

func handleQuoteCard(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		ci := msgContextInfo(evt)
		if q := ci.GetQuotedMessage(); q != nil {
			text = q.GetConversation()
			if text == "" {
				text = q.GetExtendedTextMessage().GetText()
			}
		}
	}
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sqc teks quote kamu`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Avatar sender (kalau ada) — gagal download tidak fatal.
	var avatarData []byte
	if ppURL := getProfilePicURL(ctx, evt.Info.Sender.ToNonAD()); ppURL != "" {
		if data, err := dlGetSafe(ppURL); err == nil && len(data) > 100 {
			avatarData = data
		}
	}
	senderName := evt.Info.PushName
	if senderName == "" {
		senderName = senderUser(evt)
	}

	imgData, err := renderQuoteCard(text, senderName, avatarData)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal render quote card: "+err.Error())
		return
	}
	if err := sendStickerBytes(ctx, chat, imgData, "image/png"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Quote Card renderer (lokal, tanpa API) ──────────────────────────────────
// API lama bot.lyo.su/quote/generate sudah mati (526). Render sendiri pakai
// ffmpeg drawtext + font Noto CJK (ada di sistem). Avatar bulat via ImageMagick.

const quoteFontPath = "/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc"

// escapeDrawText — escape teks untuk filter drawtext ffmpeg.
func escapeDrawText(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`:`, `\:`,
		`%`, `\%`,
		`,`, `\,`,
	)
	return r.Replace(s)
}

// wrapText — pecah teks per baris (perkiraan lebar karakter).
func wrapText(s string, maxChars int) []string {
	var lines []string
	for _, raw := range strings.Split(s, "\n") {
		words := strings.Fields(raw)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := words[0]
		for _, w := range words[1:] {
			if len(cur)+1+len(w) > maxChars {
				lines = append(lines, cur)
				cur = w
			} else {
				cur += " " + w
			}
		}
		lines = append(lines, cur)
	}
	return lines
}

func renderQuoteCard(text, name string, avatarData []byte) ([]byte, error) {
	const (
		W, H       = 512, 512
		pad        = 40
		nameSize   = 22
		quoteSize  = 26
		lineHeight = 34
		avatarSize = 72
	)
	uid := uuid.New().String()
	_ = os.MkdirAll("temp", 0o755)
	workDir := "temp"
	avatarPath := filepath.Join(workDir, "qc_av_"+uid+".png")
	avatarCircle := filepath.Join(workDir, "qc_avc_"+uid+".png")
	outPath := filepath.Join(workDir, "qc_"+uid+".png")
	defer os.Remove(avatarPath)
	defer os.Remove(avatarCircle)
	defer os.Remove(outPath)

	// 1. Avatar bulat (kalau ada) via ImageMagick.
	hasAvatar := false
	if len(avatarData) > 0 {
		if err := os.WriteFile(avatarPath, avatarData, 0o644); err == nil {
			cmd := exec.Command("magick", avatarPath,
				"-resize", fmt.Sprintf("%dx%d", avatarSize, avatarSize),
				"-gravity", "center", "-extent", fmt.Sprintf("%dx%d", avatarSize, avatarSize),
				"(", "+clone", "-alpha", "extract",
				"-draw", fmt.Sprintf("circle %d,%d %d,%d", avatarSize/2, avatarSize/2, avatarSize/2, 0),
				"-alpha", "off", ")",
				"-compose", "CopyOpacity", "-composite", avatarCircle)
			if runCmdTimeout(cmd, 30*time.Second) == nil {
				hasAvatar = true
			}
		}
	}

	// 2. Wrap teks quote.
	lines := wrapText(text, 26)
	if len(lines) > 6 {
		lines = lines[:6]
		lines = append(lines, "...")
	}
	quoteText := strings.Join(lines, "\n")

	// 3. Bangun filter drawtext.
	// Nama di samping avatar; quote di bawahnya.
	nameY := pad + avatarSize/2 - nameSize/2
	quoteY := pad + avatarSize + 20
	drawFilters := []string{
		fmt.Sprintf("drawtext=fontfile=%s:text='%s':fontcolor=0xe94560:fontsize=%d:x=%d:y=%d",
			quoteFontPath, escapeDrawText(name), nameSize, pad+avatarSize+16, nameY),
		fmt.Sprintf("drawtext=fontfile=%s:text='%s':fontcolor=white:fontsize=%d:line_spacing=%d:x=%d:y=%d",
			quoteFontPath, escapeDrawText(quoteText), quoteSize, lineHeight-quoteSize, pad, quoteY),
	}

	// 4. Render via ffmpeg.
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x1a1a2e:s=%dx%d", W, H)}
	if hasAvatar {
		// Avatar sudah di-resize bulat oleh ImageMagick → cukup overlay.
		// format=rgba wajib: tanpa itu yuv420p→rgba menggeser warna bg.
		args = append(args, "-i", avatarCircle)
		args = append(args, "-filter_complex",
			"[0:v]format=rgba[bg];[bg]"+
				strings.Join(drawFilters, ",")+
				"[t];[1:v]format=rgba[av];[t][av]overlay="+
				fmt.Sprintf("%d:%d", pad, pad))
	} else {
		args = append(args, "-vf", strings.Join(drawFilters, ","))
	}
	args = append(args, "-frames:v", "1", outPath)

	cmd := exec.Command("ffmpeg", args...)
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	return os.ReadFile(outPath)
}
