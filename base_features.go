package main

// base_features.go — port fitur dari Base-Bot-Wa (Levvi.js) ke Go.
// Command: info, myjid, owner, addowner, delowner, addprem, delprem, sticker, eval, shell.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── info ─────────────────────────────────────────────────────────────────────

func handleInfo(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat.String()
	isGroup := evt.Info.IsGroup
	fromMe := evt.Info.IsFromMe
	msgID := evt.Info.ID
	text := evt.Message.GetConversation()
	if text == "" {
		text = evt.Message.GetExtendedTextMessage().GetText()
	}
	groupLabel := "Tidak"
	if isGroup {
		groupLabel = "Ya"
	}
	fromMeLabel := "Tidak"
	if fromMe {
		fromMeLabel = "Ya"
	}
	sendText(ctx, evt.Info.Chat, fmt.Sprintf(
		"ℹ️ *INFO PESAN*\n\nJID Pengirim: %s\nJID Chat: %s\nGrup: %s\nDari Bot: %s\nID Pesan: %s\nTeks: %s",
		displayUser(ctx, evt.Info.Sender), chat, groupLabel, fromMeLabel, msgID, text,
	))
}

// ─── myjid ────────────────────────────────────────────────────────────────────

func handleMyJID(ctx context.Context, evt *events.Message) {
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("📋 JID kamu: %s", displayUser(ctx, evt.Info.Sender)))
}

// ─── owner (cek status) ───────────────────────────────────────────────────────

func handleOwnerCheck(ctx context.Context, evt *events.Message) {
	user := senderUser(evt)
	creator := isCreator(user)
	owner := isOwnerDB(user)
	var status string
	switch {
	case creator:
		status = "👑 Kamu adalah CREATOR"
	case owner:
		status = "✅ Kamu adalah OWNER"
	default:
		status = "❌ Kamu bukan owner"
	}
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("%s\n\nJID: %s\nDeveloper: %s\nCreator: %s",
		status, displayUser(ctx, evt.Info.Sender), BotDeveloper, CreatorNumber))
}

// ─── donasi / donate ──────────────────────────────────────────────────────────
// Kirim QRIS donasi — gambar di-embed ke binary (qris_embed.go), tidak perlu
// file terpisah di server.
// Cmd: !donasi, !donate, !qris

func handleDonasi(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	if len(QrisImage) == 0 {
		sendText(ctx, chat, "❌ QRIS tidak tersedia. Hubungi owner.")
		return
	}
	if err := sendImage(ctx, chat, QrisImage, "💝 Donasi\n\n> Dukung bot tetap hidup dengan donasi via QRIS di atas 🙏"); err != nil {
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "💝")
}

// ─── addowner / delowner ──────────────────────────────────────────────────────

func handleAddOwner(ctx context.Context, evt *events.Message, args string) {
	target := extractTargetNumber(evt, args)
	if target == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Format: *%saddowner 628xxx*", Prefix))
		return
	}
	db := readNumbersDB(OwnerDBPath)
	if containsNumber(db, target) {
		sendText(ctx, evt.Info.Chat, "⚠️ Sudah jadi owner: "+target)
		return
	}
	db = append(db, target)
	if err := saveNumbersDB(OwnerDBPath, db); err != nil {
		sendText(ctx, evt.Info.Chat, "❌ Gagal simpan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, "✅ Berhasil add owner\n+"+target)
}

func handleDelOwner(ctx context.Context, evt *events.Message, args string) {
	target := extractTargetNumber(evt, args)
	if target == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Format: *%sdelowner 628xxx*", Prefix))
		return
	}
	db := readNumbersDB(OwnerDBPath)
	newDB := removeNumber(db, target)
	if len(newDB) == len(db) {
		sendText(ctx, evt.Info.Chat, "⚠️ Nomor tidak ada di daftar owner: "+target)
		return
	}
	if err := saveNumbersDB(OwnerDBPath, newDB); err != nil {
		sendText(ctx, evt.Info.Chat, "❌ Gagal simpan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, "✅ Berhasil del owner\n+"+target)
}

// ─── addprem / delprem ────────────────────────────────────────────────────────

func handleAddPrem(ctx context.Context, evt *events.Message, args string) {
	target := extractTargetNumber(evt, args)
	if target == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Format: *%saddprem 628xxx*", Prefix))
		return
	}
	db := readNumbersDB(PremiumDBPath)
	if containsNumber(db, target) {
		sendText(ctx, evt.Info.Chat, "⚠️ Sudah premium: "+target)
		return
	}
	db = append(db, target)
	if err := saveNumbersDB(PremiumDBPath, db); err != nil {
		sendText(ctx, evt.Info.Chat, "❌ Gagal simpan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, "✅ Berhasil add premium\n+"+target)
}

func handleDelPrem(ctx context.Context, evt *events.Message, args string) {
	target := extractTargetNumber(evt, args)
	if target == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Format: *%sdelprem 628xxx*", Prefix))
		return
	}
	db := readNumbersDB(PremiumDBPath)
	newDB := removeNumber(db, target)
	if len(newDB) == len(db) {
		sendText(ctx, evt.Info.Chat, "⚠️ Nomor tidak ada di daftar premium: "+target)
		return
	}
	if err := saveNumbersDB(PremiumDBPath, newDB); err != nil {
		sendText(ctx, evt.Info.Chat, "❌ Gagal simpan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, "✅ Berhasil del premium\n+"+target)
}

// ─── eval (shell) ─────────────────────────────────────────────────────────────
// Khusus creator. Jalanin shell command. Sama kayak "$ command" di Base-Bot-Wa.

func handleShell(ctx context.Context, evt *events.Message, args string) {
	if args == "" {
		sendText(ctx, evt.Info.Chat, "❌ Contoh: *"+Prefix+"shell ls -la*")
		return
	}
	reactMsg(ctx, evt, "⏳")

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", args)
	} else {
		cmd = exec.Command("sh", "-c", args)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Timeout 30 detik — sama kayak Base-Bot-Wa
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		sendText(ctx, evt.Info.Chat, "⏱️ Timeout 30s.")
		return
	}

	output := out.String()
	if output == "" {
		if runErr != nil {
			output = "❌ Error: " + runErr.Error()
		} else {
			output = "✅ Selesai (tidak ada output)"
		}
	}
	if len(output) > 2000 {
		output = output[:2000] + "\n... (output dipotong)"
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, "💻 *Output:*\n```\n"+output+"\n```")
}

// ─── sticker ──────────────────────────────────────────────────────────────────
// Reply gambar/video/audio → kirim balik sebagai sticker.
// Butuh ffmpeg di PATH (sudah jadi requirement YuukiBot).

func handleSticker(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat

	// Ambil quoted message
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, "❌ Reply ke gambar/video dulu baru ketik *"+Prefix+"sticker*")
		return
	}

	// Tentukan tipe media
	var mediaMsg interface{ GetDirectPath() string }
	var mediaType string
	if img := quoted.GetImageMessage(); img != nil {
		mediaMsg = img
		mediaType = "image"
	} else if vid := quoted.GetVideoMessage(); vid != nil {
		mediaMsg = vid
		mediaType = "video"
	} else {
		sendText(ctx, chat, "❌ Hanya gambar atau video yang bisa dijadikan sticker.")
		return
	}
	_ = mediaMsg

	reactMsg(ctx, evt, "⏳")

	// Download raw bytes
	var rawBytes []byte
	var err error
	switch mediaType {
	case "image":
		rawBytes, err = waClient.Download(ctx, quoted.GetImageMessage())
	case "video":
		rawBytes, err = waClient.Download(ctx, quoted.GetVideoMessage())
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media: "+err.Error())
		return
	}

	// Buat temp dir
	if err := os.MkdirAll("temp", 0o755); err != nil {
		sendText(ctx, chat, "❌ Gagal buat temp dir")
		return
	}

	uid := uuid.New().String()
	var inPath, outPath string

	switch mediaType {
	case "image":
		inPath = filepath.Join("temp", "sticker_in_"+uid+".jpg")
		outPath = filepath.Join("temp", "sticker_out_"+uid+".webp")
		if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
			sendText(ctx, chat, "❌ Gagal tulis file temp: "+err.Error())
			return
		}
		defer os.Remove(inPath)
		defer os.Remove(outPath)
		// ffmpeg: gambar → webp, crop ke square, resize 512x512
		var stderr bytes.Buffer
		cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
			"-i", inPath,
			"-vf", "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000",
			"-c:v", "libwebp", "-lossless", "0", "-q:v", "80", "-preset", "picture",
			outPath)
		cmd.Stderr = &stderr
		// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
		if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal buat sticker (ffmpeg): "+lastLines(stderr.String(), 3))
			return
		}

	case "video":
		inPath = filepath.Join("temp", "sticker_in_"+uid+".mp4")
		outPath = filepath.Join("temp", "sticker_out_"+uid+".webp")
		if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
			sendText(ctx, chat, "❌ Gagal tulis file temp: "+err.Error())
			return
		}
		defer os.Remove(inPath)
		defer os.Remove(outPath)
		// ffmpeg: video → animated webp, max 6 detik, 512x512
		var stderr bytes.Buffer
		cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
			"-i", inPath, "-t", "6",
			"-vf", "fps=15,scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000",
			"-c:v", "libwebp_anim", "-lossless", "0", "-q:v", "70", "-loop", "0",
			outPath)
		cmd.Stderr = &stderr
		// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
		if err := runCmdTimeout(cmd, 90*time.Second); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal buat sticker (ffmpeg): "+lastLines(stderr.String(), 3))
			return
		}
	}

	// Baca hasil webp
	webpBytes, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil sticker: "+err.Error())
		return
	}

	// Upload ke WA server — StickerMessage pakai MediaImage (bukan "sticker").
	// Ref: whatsmeow/download.go classToMediaType["StickerMessage"] = MediaImage
	uploaded, err := waClient.Upload(ctx, webpBytes, whatsmeow.MediaImage)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload sticker: "+err.Error())
		return
	}

	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(webpBytes))),
			Mimetype:      proto.String("image/webp"),
		},
	})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim sticker: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "✅")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// extractTargetNumber ambil nomor dari args. Strip semua non-digit, hapus prefix +.
// Kalau args kosong, return "".
func extractTargetNumber(_ *events.Message, args string) string {
	raw := strings.TrimSpace(args)
	if raw == "" {
		return ""
	}
	// Kalau ada mention @xxx, ambil prefix numerik sebelum @
	if idx := strings.Index(raw, "@"); idx != -1 {
		raw = raw[:idx]
	}
	// Hapus semua non-digit
	var b strings.Builder
	for _, c := range raw {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	num := strings.TrimPrefix(b.String(), "+")
	if len(num) < 8 {
		return ""
	}
	return num
}

// lastLines ada di videoencode.go — tidak perlu didefinisikan ulang di sini.
