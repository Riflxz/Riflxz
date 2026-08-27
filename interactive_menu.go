package main

// interactive_menu.go — menu interaktif WhatsApp (native flow buttons).
//
// Ganti versi menu saat runtime: !setmenu 0 / 1 / 2 / 3 / 4
//   0 = plain text  (paling kompatibel)
//   1 = Interactive + header + native flow buttons  ← DEFAULT
//   2 = Interactive teks + quick_reply sederhana
//
// Button click ditangani di handleMessage via InteractiveResponseMessage.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Menu image cache (canvas) ────────────────────────────────────────────────
// Download & upload gambar header sekali, cache selamanya selama bot jalan.
// Port dari imageMessage di header menu.js.

// menuImgCache hasil upload gambar header + mimetype hasil deteksi.
type menuImgCache struct {
	up       *whatsmeow.UploadResponse
	mimetype string
}

var (
	menuImgMu       sync.Mutex
	menuImgUploaded *menuImgCache
)

// sniffImageMimetype deteksi tipe gambar dari magic bytes (3-8 byte pertama).
// Dukung PNG/JPEG/GIF/WebP — fallback "" kalau tidak dikenal.
func sniffImageMimetype(data []byte) string {
	if len(data) >= 8 {
		if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
			data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A {
			return "image/png"
		}
		if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
			return "image/gif"
		}
		// WebP: byte 0-3 = "RIFF", byte 8-11 = "WEBP"
		if data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
			data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
			return "image/webp"
		}
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	return ""
}

// getMenuImage download dan upload gambar header menu, cache selamanya.
// Return nil kalau MenuImageURL kosong, gagal, atau ukuran > 10MB.
// Timeout download 15 detik supaya tidak menggantung.
func getMenuImage(ctx context.Context) *menuImgCache {
	if MenuImageURL == "" {
		return nil
	}
	menuImgMu.Lock()
	defer menuImgMu.Unlock()
	if menuImgUploaded != nil {
		return menuImgUploaded
	}

	const maxSize = 10 * 1024 * 1024 // batas ukuran download: 10MB
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(MenuImageURL)
	if err != nil {
		pool.logger.Warn().Err(err).Msg("menu image: gagal download")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		pool.logger.Warn().Int("status", resp.StatusCode).Msg("menu image: status tidak OK")
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		pool.logger.Warn().Err(err).Msg("menu image: gagal baca body")
		return nil
	}
	if len(data) == 0 {
		pool.logger.Warn().Msg("menu image: data kosong")
		return nil
	}
	if len(data) > maxSize {
		pool.logger.Warn().Msg("menu image: ukuran melebihi 10MB, dibatalkan")
		return nil
	}

	// Deteksi mimetype: magic bytes lebih diutamakan, Content-Type cadangan.
	mimetype := sniffImageMimetype(data)
	if mimetype == "" {
		mimetype = resp.Header.Get("Content-Type")
		if i := strings.IndexByte(mimetype, ';'); i >= 0 {
			mimetype = strings.TrimSpace(mimetype[:i])
		}
	}
	if mimetype == "" || !strings.HasPrefix(mimetype, "image/") {
		mimetype = "image/jpeg"
	}

	// Upload ke WA
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		pool.logger.Warn().Err(err).Msg("menu image: gagal upload ke WA")
		return nil
	}
	menuImgUploaded = &menuImgCache{up: &up, mimetype: mimetype}
	pool.logger.Debug().Str("url", up.URL).Str("mimetype", mimetype).Msg("menu image: uploaded & cached")
	return menuImgUploaded
}

// buildImageHeader buat InteractiveMessage_Header dengan gambar kalau tersedia.
func buildImageHeader(ctx context.Context, title, subtitle string) *waE2E.InteractiveMessage_Header {
	img := getMenuImage(ctx)
	if img == nil {
		return &waE2E.InteractiveMessage_Header{
			Title:              proto.String(title),
			Subtitle:           proto.String(subtitle),
			HasMediaAttachment: proto.Bool(false),
		}
	}
	// Mimetype hasil sniff magic bytes (PNG/JPEG/GIF/WebP) — jangan hardcode.
	mime := img.mimetype
	if mime == "" {
		mime = "image/jpeg"
	}
	return &waE2E.InteractiveMessage_Header{
		Title:              proto.String(title),
		Subtitle:           proto.String(subtitle),
		HasMediaAttachment: proto.Bool(true),
		Media: &waE2E.InteractiveMessage_Header_ImageMessage{
			ImageMessage: &waE2E.ImageMessage{
				URL:           proto.String(img.up.URL),
				DirectPath:    proto.String(img.up.DirectPath),
				MediaKey:      img.up.MediaKey,
				FileEncSHA256: img.up.FileEncSHA256,
				FileSHA256:    img.up.FileSHA256,
				FileLength:    proto.Uint64(img.up.FileLength),
				Mimetype:      proto.String(mime),
			},
		},
	}
}

// channelFooter balikin teks footer yang menampilkan nama channel.
func channelFooter() string {
	if ChannelName != "" {
		return fmt.Sprintf("%s  •  %s", ChannelName, BotDeveloper)
	}
	return BotDeveloper
}

// ─── Newsletter context (forwarded from channel badge) ────────────────────────
// Fix "update not available" / "update deleted":
// Fetch message yang benar-benar valid (Message != nil) dari channel.
// Di-cache 30 menit.

var (
	nlMsgIDMu   sync.Mutex
	nlMsgID     int
	nlMsgIDTime int64
	nlMsgIDTTL  int64 = 1800 // 30 menit
)

func fetchValidNewsletterMsgID(ctx context.Context) int {
	if SwGC2JID == "" {
		return 0
	}
	nlMsgIDMu.Lock()
	defer nlMsgIDMu.Unlock()

	now := time.Now().Unix()
	if nlMsgID > 0 && (now-nlMsgIDTime) < nlMsgIDTTL {
		return nlMsgID
	}

	jid, err := types.ParseJID(SwGC2JID)
	if err != nil {
		return 0
	}
	// Ambil 10 pesan terbaru, pilih yang Message-nya tidak nil (belum dihapus)
	msgs, err := waClient.GetNewsletterMessages(ctx, jid,
		&whatsmeow.GetNewsletterMessagesParams{Count: 10})
	if err != nil || len(msgs) == 0 {
		return 0
	}
	for _, m := range msgs {
		if m.Message != nil && m.MessageServerID > 0 {
			nlMsgID = m.MessageServerID
			nlMsgIDTime = now
			return nlMsgID
		}
	}
	// Fallback: pakai ID pertama walau mungkin dihapus
	if msgs[0].MessageServerID > 0 {
		nlMsgID = msgs[0].MessageServerID
		nlMsgIDTime = now
		return nlMsgID
	}
	return 0
}

// newsletterCtxInfo buat ContextInfo "Diteruskan dari [channel]".
// Pakai real MessageServerID dari channel — biar bisa diklik dan navigate ke channel.
func newsletterCtxInfo(ctx context.Context) *waE2E.ContextInfo {
	if SwGC2JID == "" {
		return nil
	}
	msgID := fetchValidNewsletterMsgID(ctx)
	if msgID == 0 {
		return nil
	}
	return &waE2E.ContextInfo{
		ForwardingScore: proto.Uint32(999),
		IsForwarded:     proto.Bool(true),
		ForwardedNewsletterMessageInfo: &waE2E.ContextInfo_ForwardedNewsletterMessageInfo{
			NewsletterJID:   proto.String(SwGC2JID),
			ServerMessageID: proto.Int32(int32(msgID)),
			NewsletterName:  proto.String(ChannelName),
		},
	}
}

// bizNodes adalah base additional node untuk InteractiveMessage.
// Equivalent dari additionalNodes di Baileys relayMessage.
var bizNodes = []waBinary.Node{
	{
		Tag:   "biz",
		Attrs: waBinary.Attrs{},
		Content: []waBinary.Node{
			{
				Tag: "interactive",
				Attrs: waBinary.Attrs{
					"type": "native_flow",
					"v":    "1",
				},
				Content: []waBinary.Node{
					{
						Tag: "native_flow",
						Attrs: waBinary.Attrs{
							"v":    "9",
							"name": "mixed",
						},
					},
				},
			},
		},
	},
}

// buildBizNodes buat additionalNodes sesuai tipe chat.
// Port dari menu2.js: kalau bukan grup, tambah node "bot" dengan biz_bot=1.
// Ini penting agar interactive message bisa diklik di DM.
func buildBizNodes(chat types.JID) []waBinary.Node {
	nodes := append([]waBinary.Node(nil), bizNodes...)
	// Kalau bukan grup — tambah bot node (seperti menu2.js baris 115-119)
	if chat.Server != types.GroupServer {
		nodes = append(nodes, waBinary.Node{
			Tag:   "bot",
			Attrs: waBinary.Attrs{"biz_bot": "1"},
		})
	}
	return nodes
}

// ─── Menu version state ───────────────────────────────────────────────────────

var (
	menuVersionMu sync.RWMutex
	menuVersion   = 3 // default V3 (single_select list — paling rapi)
)

func getMenuVersion() int {
	menuVersionMu.RLock()
	defer menuVersionMu.RUnlock()
	return menuVersion
}

func setMenuVersion(v int) {
	menuVersionMu.Lock()
	menuVersion = v
	menuVersionMu.Unlock()
}

// ─── Kirim menu sesuai versi ──────────────────────────────────────────────────

// menuGreeting — sapaan pembuka menu: "Halo @tag" + role user.
// Di grup pakai mention (@nomor), di PM tanpa tag. Role: Owner/Premium/User.
func menuGreeting(evt *events.Message, ownerSender, ownerLevel, premiumSender bool) string {
	role := "User"
	switch {
	case ownerSender:
		role = "Owner"
	case premiumSender:
		role = "Premium"
	}
	if evt != nil && evt.Info.IsGroup {
		return fmt.Sprintf("Halo @%s 👋\n◈ Role : %s\n\n", senderUser(evt), role)
	}
	return fmt.Sprintf("Halo! 👋\n◈ Role : %s\n\n", role)
}

// menuMentionJID — JID buat MentionedJID kalau greeting memuat @tag (grup).
func menuMentionJID(evt *events.Message) (types.JID, bool) {
	if evt == nil || !evt.Info.IsGroup {
		return types.JID{}, false
	}
	user := senderUser(evt)
	if user == "" {
		return types.JID{}, false
	}
	return types.NewJID(user, types.DefaultUserServer), true
}

// menuCtxInfo — ContextInfo pesan menu: newsletter ctx + MentionedJID (grup).
func menuCtxInfo(ctx context.Context, evt *events.Message) *waE2E.ContextInfo {
	ci := newsletterCtxInfo(ctx)
	if mj, ok := menuMentionJID(evt); ok {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.MentionedJID = []string{mj.String()}
	}
	return ci
}

// sendMenuText — kirim menu plain-text (default/V1/V2 fallback) dengan
// greeting + mention.
func sendMenuText(ctx context.Context, chat types.JID, evt *events.Message, ownerSender, ownerLevel, premiumSender bool) {
	body := menuGreeting(evt, ownerSender, ownerLevel, premiumSender) +
		menuText(ownerSender, ownerLevel, premiumSender)
	sendTextWithCtx(ctx, chat, body, menuCtxInfo(ctx, evt))
}

func sendMenu(ctx context.Context, chat types.JID, evt *events.Message, ownerSender, ownerLevel, premiumSender bool) {
	v := getMenuVersion()
	pool.logger.Debug().Int("version", v).Str("chat", chat.String()).Msg("sendMenu dipanggil")

	switch v {
	case 1:
		sendMenuV1(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	case 2:
		sendMenuV2(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	case 3:
		sendMenuV3(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	default:
		pool.logger.Info().Msg("sendMenu: plain text")
		sendMenuText(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	}
}

// ─── Helper: buat tombol quick_reply ─────────────────────────────────────────

func qrButton(label, id string) *waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"id":           id,
	})
	return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("quick_reply"),
		ButtonParamsJSON: proto.String(string(params)),
	}
}

// ctaURL buat tombol link eksternal.
func ctaURL(label, url string) *waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton {
	params, _ := json.Marshal(map[string]string{
		"display_text": label,
		"url":          url,
		"merchant_url": url,
	})
	return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("cta_url"),
		ButtonParamsJSON: proto.String(string(params)),
	}
}

// ─── Menu V1 — InteractiveMessage + biz node (port menu.js) ─────────────────
// Sekarang bisa inject bizNodes via SendRequestExtra.AdditionalNodes —
// whatsmeow sudah support ini, kita tinggal pakai.

func sendMenuV1(ctx context.Context, chat types.JID, evt *events.Message, ownerSender, ownerLevel, premiumSender bool) {
	p := Prefix
	body := menuGreeting(evt, ownerSender, ownerLevel, premiumSender) + menuText(ownerSender, ownerLevel, premiumSender)
	ci := menuCtxInfo(ctx, evt)

	// WhatsApp native flow: maksimal 3 tombol quick_reply per pesan.
	b := NewMsgBuilder().
		SetHeader(BotName, fmt.Sprintf("by %s  •  prefix: %s", BotDeveloper, p)).
		SetImageHeader(getMenuImage(ctx)).
		SetBody(body).
		SetFooter(channelFooter()).
		SetContextInfo(ci).
		AddQRButton("Menu", p+"menu").
		AddQRButton("Contributor", p+"contributor").
		AddQRButton("Info", p+"info")
	if ownerSender || premiumSender {
		b = NewMsgBuilder().
			SetHeader(BotName, fmt.Sprintf("by %s  •  prefix: %s", BotDeveloper, p)).
			SetImageHeader(getMenuImage(ctx)).
			SetBody(body).
			SetFooter(channelFooter()).
			SetContextInfo(ci).
			AddQRButton("Menu", p+"menu").
			AddQRButton("Play", p+"play").
			AddQRButton("Self", p+"self")
	}

	pool.logger.Info().Str("chat", chat.String()).Msg("sendMenuV1: mencoba kirim InteractiveMessage + biz node")
	if err := b.Send(ctx, chat); err != nil {
		pool.logger.Warn().Err(err).Msg("sendMenuV1: gagal, fallback ke plain text")
		sendMenuText(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	} else {
		pool.logger.Debug().Msg("sendMenuV1: berhasil dikirim")
	}
}

// ─── Menu V2 — InteractiveMessage ringkas + biz node (port menu2.js) ─────────

func sendMenuV2(ctx context.Context, chat types.JID, evt *events.Message, ownerSender, ownerLevel, premiumSender bool) {
	p := Prefix

	mode := getMode()
	modeStr := "Public"
	if mode == "self" {
		modeStr = "Self"
	}
	senders := pool.list()
	online := 0
	for _, s := range senders {
		if s.connected() {
			online++
		}
	}
	body := menuGreeting(evt, ownerSender, ownerLevel, premiumSender) + fmt.Sprintf(
		"*%s*  _%s_\n\n"+
			"Mode  : %s\n"+
			"Sender: %d/%d online\n"+
			"Uptime: %s\n\n"+
			"Ketik *%smenu* untuk daftar lengkap.",
		BotName, BotDeveloper,
		modeStr, online, len(senders),
		formatDuration(time.Since(startTime)),
		p,
	)
	ci := menuCtxInfo(ctx, evt)

	b := NewMsgBuilder().
		SetHeader(BotName, fmt.Sprintf("by %s", BotDeveloper)).
		SetImageHeader(getMenuImage(ctx)).
		SetBody(body).
		SetFooter(channelFooter()).
		SetContextInfo(ci).
		AddQRButton("Menu Lengkap", p+"menu").
		AddQRButton("Contributor", p+"contributor").
		AddQRButton("Ping", p+"ping")
	if ownerSender {
		// Premium tetap di builder dasar (Menu Lengkap/Ping/Play); tombol
		// Senders owner-only.
		b = NewMsgBuilder().
			SetHeader(BotName, fmt.Sprintf("by %s", BotDeveloper)).
			SetImageHeader(getMenuImage(ctx)).
			SetBody(body).
			SetFooter(channelFooter()).
			SetContextInfo(ci).
			AddQRButton("Menu Lengkap", p+"menu").
			AddQRButton("Play", p+"play").
			AddQRButton("Senders", p+"ls")
	}

	pool.logger.Info().Str("chat", chat.String()).Msg("sendMenuV2: mencoba kirim InteractiveMessage + biz node")
	if err := b.Send(ctx, chat); err != nil {
		pool.logger.Warn().Err(err).Msg("sendMenuV2: gagal, fallback ke plain text")
		sendMenuText(ctx, chat, evt, ownerSender, ownerLevel, premiumSender)
	} else {
		pool.logger.Debug().Msg("sendMenuV2: berhasil dikirim")
	}
}

// ─── Menu V3 — single_select list (port index.js sendInteractiveMenu) ────────
// Paling rapi: satu tombol "Pilih Command" → dropdown list berisi semua command.
// Fallback: ListMessage → plain text. DEFAULT.

type listRow struct {
	header string // label kategori di atas baris
	title  string // nama command (ditampilkan)
	desc   string // deskripsi singkat
	id     string // command yang dieksekusi saat dipilih (dengan prefix)
}

type listSection struct {
	title string
	rows  []listRow
}

// howto: prefix dipakai untuk command yang butuh input manual.
// Saat diklik, bot kirim panduan cara pakai — bukan error.
const howtoPrefix = "howto:"

// howtoText balikin panduan cara pakai per command.
var howtoText = map[string]string{
	"play": "📞 *Cara pakai* `!play`:\n\n" +
		"• Reply chat target + nama lagu:\n  _reply pesan → `!play another love`_\n\n" +
		"• Langsung dengan nomor:\n  `!play 628xxx, another love`\n\n" +
		"• Sudah dalam call (tambah antrian):\n  `!play another love`",

	"video": "🎬 *Cara pakai* `!video`:\n\n" +
		"• Paste link YouTube:\n  `!video https://youtu.be/xxx`\n\n" +
		"• Hanya mendukung link YouTube.",

	"tt": "⬇️ *Cara pakai* `!tt` (TikTok):\n\n" +
		"• Link langsung:\n  `!tt https://vm.tiktok.com/xxx`\n\n" +
		"• Cari by judul:\n  `!tt nama lagu atau video`",

	"ytmp3": "⬇️ *Cara pakai* `!ytmp3` (YouTube MP3):\n\n" +
		"• Link YouTube:\n  `!ytmp3 https://youtu.be/xxx`\n\n" +
		"• Cari by judul:\n  `!ytmp3 another love tom odell`",

	"ytplay": "⬇️ *Cara pakai* `!ytplay` (YouTube video + audio):\n\n" +
		"• Cari by judul:\n  `!ytplay another love`\n\n" +
		"• Kirim video MP4 + audio MP3 ke chat.",

	"bulk": "🎞️ *Cara pakai* `!bulk` (Bulk Alight Motion):\n\n" +
		"• `!bulk 1` — buat 1 akun Alight Motion premium otomatis\n\n" +
		"• Proses ±1 menit (email sementara + aktivasi via magic link).\n" +
		"• Maksimal 1 akun per command.",

	"ig": "⬇️ *Cara pakai* `!ig` (Instagram):\n\n" +
		"• Link post/reel:\n  `!ig https://www.instagram.com/p/xxx/`\n\n" +
		"• Support foto, video, dan reel",

	"fb": "⬇️ *Cara pakai* `!fb` (Facebook):\n\n" +
		"• Link video:\n  `!fb https://www.facebook.com/watch?v=xxx`\n\n" +
		"• Atau link reel Facebook",

	"soundcloud": "⬇️ *Cara pakai* `!soundcloud` (SoundCloud):\n\n" +
		"• Link track:\n  `!soundcloud https://soundcloud.com/xxx`\n\n" +
		"• Atau cari by judul:\n  `!soundcloud another love`",

	"yts": "🔎 *Cara pakai* `!yts`:\n\n" +
		"• `!yts another love`\n" +
		"• Menampilkan 5 hasil YouTube beserta link.",

	"pinterest": "🔎 *Cara pakai* `!pinterest`:\n\n" +
		"• `!pinterest anime landscape` — muncul carousel hasil, tap *Kirim* di kartu yang disukai\n" +
		"• `!pinpick 2` — kirim ulang hasil nomor 2 (dari carousel terakhir)",

	"bingimage": "🔎 *Cara pakai* `!bingimage`:\n\n" +
		"• `!bingimage cyberpunk city`\n" +
		"• Bot mengirim hingga 5 gambar.",

	"applemusic": "🔎 *Cara pakai* `!applemusic`:\n\n" +
		"• `!applemusic another love`\n" +
		"• Menampilkan 5 lagu dari Apple Music.",

	"wiki": "🔎 *Cara pakai* `!wiki`:\n\n" +
		"• `!wiki Indonesia`\n" +
		"• Menampilkan ringkasan artikel Wikipedia.",

	"kbbi": "🔎 *Cara pakai* `!kbbi`:\n\n" +
		"• `!kbbi narasi`\n" +
		"• Menampilkan definisi kata dari KBBI.",

	"lyrics": "🔎 *Cara pakai* `!lyrics`:\n\n" +
		"• `!lyrics another love`\n" +
		"• Menampilkan lirik, artis, dan album.",

	"barcode": "🔧 *Cara pakai* `!barcode`:\n\n" +
		"• `!barcode 1234567890`\n" +
		"• `!barcode teks apapun`",

	"tempmail": "📧 *Cara pakai* `!tempmail`:\n\n" +
		"• `!tempmail` — buat email baru\n" +
		"• `!cekmail <token>` — cek inbox",

	"whatmusic": "🎵 *Cara pakai* `!whatmusic`:\n\n" +
		"• Reply audio/voice note → `!whatmusic`\n" +
		"• Bot akan identifikasi judul & artis lagu",

	"idch": "📡 *Cara pakai* `!idch`:\n\n" +
		"• `!idch https://whatsapp.com/channel/xxx`\n" +
		"• Tampilkan ID saluran WA",

	"getidch": "📡 *Cara pakai* `!getidch` (semua user):\n\n" +
		"• `!getidch https://whatsapp.com/channel/xxx`\n" +
		"• Bot kirim ID, nama, dan jumlah subscriber saluran",

	"idgc": "👥 *Cara pakai* `!idgc`:\n\n" +
		"• `!idgc https://chat.whatsapp.com/xxx`\n" +
		"• Tampilkan ID & info grup WA",

	"cap": "🎨 *Cara pakai* `!cap`:\n\n" +
		"• Reply gambar + ketik:\n  `!cap Teks caption di sini`",

	"compress": "🗜️ *Cara pakai* `!compress`:\n\n" +
		"• Reply gambar → `!compress` (kompres gambar)\n" +
		"• Reply video → `!compress` (kompres video)",

	"tofile": "🔄 *Cara pakai* `!tofile`:\n\n" +
		"• Reply teks → `!tofile script.py`\n" +
		"• Reply dokumen → `!tofile output.txt`\n" +
		"• Extension yang didukung: .py .js .json .txt .md .html dll",

	"resend": "📎 *Cara pakai* `!resend`:\n\n" +
		"• Reply foto/video → `!resend`\n" +
		"• Bot kirim ulang media *tanpa kompres* — tetap bisa dilihat/diputar\n" +
		"• Bytes yang diterima dikirim ulang apa adanya, kualitas tidak diturunkan",

	"bl": "🚫 *Cara pakai* `!bl` (blacklist):\n\n" +
		"• `!bl add 628xxx` — blokir nomor\n" +
		"• `!bl add` (di dalam grup) — blokir seluruh grup\n" +
		"• `!bl del 628xxx` / `!bl del` (di dalam grup) — buka blokir\n" +
		"• `!bl list` — lihat daftar user & grup",

	"clear": "🧹 *Cara pakai* `!clear`:\n\n" +
		"• Bersihkan file cache di temp/\n" +
		"• File yang masih aktif (< 5 menit) dipertahankan",

	"jaga": "🛡️ *Cara pakai* `!jaga` (admin grup):\n\n" +
		"• `!jaga` — lihat status jaga grup\n" +
		"• `!jaga on` / `!jaga off` — aktifkan/matikan semua proteksi",

	"antilink": "🔗 *Cara pakai* `!antilink` (admin grup):\n\n" +
		"• `!antilink on` — peringatkan pesan ber-link\n" +
		"• `!antilink off` — matikan",

	"antitoxic": "🚫 *Cara pakai* `!antitoxic` (admin grup):\n\n" +
		"• `!antitoxic on` — peringatkan kata kasar\n" +
		"• `!antitoxic off` — matikan",

	"welcome": "🎉 *Cara pakai* `!welcome` (admin grup):\n\n" +
		"• `!welcome on` — sambut member baru\n" +
		"• `!welcome off` — matikan",

	"setwelcome": "✍️ *Cara pakai* `!setwelcome` (admin grup):\n\n" +
		"• `!setwelcome Halo @user, selamat datang di @group!`\n" +
		"• `@user` = mention member baru, `@group` = nama grup\n" +
		"• `!setwelcome` (tanpa teks) — kembalikan ke pesan default",

	"warn": "⚠️ *Cara pakai* `!warn` (admin grup):\n\n" +
		"• Reply pesan member → `!warn`\n" +
		"• 3x warn = kick otomatis\n" +
		"• Lihat daftar: `!warnlist`",

	"warnlist": "📋 *Cara pakai* `!warnlist` (admin grup):\n\n" +
		"• `!warnlist` — lihat semua member yang kena warn\n" +
		"• Reset: `!resetwarn all`",

	"kick": "👢 *Cara pakai* `!kick` (admin grup):\n\n" +
		"• Reply pesan member → `!kick`\n" +
		"• `!kick 628xxx` — keluarkan via nomor\n" +
		"• Bisa multi: `!kick 628xxx 628yyy`\n" +
		"• Admin & owner bot tidak bisa di-kick",

	"close": "🔒 *Cara pakai* `!close` (admin grup):\n\n" +
		"• `!close` — kunci grup (hanya admin bisa chat)\n" +
		"• Buka lagi dengan `!open`",

	"open": "🔓 *Cara pakai* `!open` (admin grup):\n\n" +
		"• `!open` — buka grup untuk semua member\n" +
		"• Kebalikan dari `!close`",

	"add": "➕ *Cara pakai* `!add` (admin grup):\n\n" +
		"• `!add 628xxx` — tambah member via nomor\n" +
		"• Bisa beberapa nomor sekaligus, pisah spasi",

	"promote": "⬆️ *Cara pakai* `!promote` (admin grup):\n\n" +
		"• Reply pesan member → `!promote`\n" +
		"• Atau `!promote 628xxx` — angkat jadi admin",

	"demote": "⬇️ *Cara pakai* `!demote` (admin grup):\n\n" +
		"• Reply pesan member → `!demote`\n" +
		"• Atau `!demote 628xxx` — turunkan jadi member",

	"tagall": "📢 *Cara pakai* `!tagall` (admin grup):\n\n" +
		"• `!tagall` — tag semua member\n" +
		"• `!tagall pesan` — tag semua + pesan",

	"hidetag": "🤫 *Cara pakai* `!hidetag` (admin grup):\n\n" +
		"• `!hidetag pesan` — kirim pesan + tag semua member\n" +
		"• Tanpa teks: `!hidetag` — kirim 📢 + tag semua",

	"setname": "✏️ *Cara pakai* `!setname` (admin grup):\n\n" +
		"• `!setname Nama Baru` — ganti nama grup",

	"setdesc": "📝 *Cara pakai* `!setdesc` (admin grup):\n\n" +
		"• `!setdesc Deskripsi baru` — ganti deskripsi grup",

	"setppgc": "🖼️ *Cara pakai* `!setppgc` (admin grup):\n\n" +
		"• Reply gambar → `!setppgc` — ganti foto grup",

	"linkgc": "🔗 *Cara pakai* `!linkgc` (admin grup):\n\n" +
		"• `!linkgc` — ambil link undangan grup\n" +
		"• Link lama mati: `!revoke`",

	"revoke": "🔄 *Cara pakai* `!revoke` (admin grup):\n\n" +
		"• `!revoke` — reset link undangan (link lama tidak berlaku)\n" +
		"• Bot kirim link baru",

	"infogc": "ℹ️ *Cara pakai* `!infogc` (admin grup):\n\n" +
		"• `!infogc` — info grup: nama, ID, owner, dibuat, member, admin",

	"out": "👋 *Cara pakai* `!out` (admin grup):\n\n" +
		"• `!out` — bot keluar dari grup ini",

	"resetwarn": "🔄 *Cara pakai* `!resetwarn` (admin grup):\n\n" +
		"• Reply member → `!resetwarn` — reset warn member itu\n" +
		"• `!resetwarn all` — reset warn semua member",

	"jpm": "📣 *Cara pakai* `!jpm` (khusus owner):\n\n" +
		"• `!jpm` — buka menu broadcast (pilih mode: basic / hidetag / channel / update / auto)\n" +
		"• `!jpm teks` — simpan teks dulu, lalu pilih mode dari menu\n" +
		"• Reply media (foto/video/audio) → `!jpm` — broadcast media\n" +
		"• `!stopjpm` — hentikan broadcast yang berjalan",

	"jpmht": "📣 *Cara pakai* `!jpmht` (khusus owner):\n\n" +
		"• `!jpmht teks` — broadcast + tag semua member di tiap grup\n" +
		"• Reply media → `!jpmht` — broadcast media + tag semua member",

	"jpmch": "📣 *Cara pakai* `!jpmch` (khusus owner):\n\n" +
		"• `!jpmch teks` — broadcast teks ke semua saluran yang diikuti\n" +
		"• Reply media → `!jpmch` — broadcast media ke semua saluran",

	"autojpm": "📣 *Cara pakai* `!autojpm` (khusus owner):\n\n" +
		"• `!autojpm on` — nyalakan auto-broadcast (interval default 30 menit)\n" +
		"• `!autojpm on 1j` — interval custom (15m/30m/1j/2j/6j/12j/1h/1d)\n" +
		"• `!autojpm off` — matikan\n" +
		"• `!autojpm status` — lihat jadwal & pesan tersimpan\n" +
		"• Reply media → `!autojpm on` — simpan media sebagai konten auto",

	"setdelayjpm": "⏱️ *Cara pakai* `!setdelayjpm` (khusus owner):\n\n" +
		"• `!setdelayjpm` — pilih jeda dari menu (1-15 detik)\n" +
		"• `!setdelayjpm 20000` — jeda custom dalam milidetik (1000-30000)\n" +
		"• Default: 5 detik antar grup",

	"bljpm": "🚫 *Cara pakai* `!bljpm` (khusus owner):\n\n" +
		"• `!bljpm` — lihat daftar grup yang diblacklist dari JPM\n" +
		"• `!bljpm 1 3 5` — toggle blacklist grup (pakai nomor dari daftar)\n" +
		"• `!blautojpm` — blacklist khusus auto-JPM (cara pakai sama)",

	"jpmupdate": "📣 *Cara pakai* `!jpmupdate` (khusus owner):\n\n" +
		"• `!jpmupdate` — broadcast info update bot ke semua grup\n" +
		"• `!jpmupdate 1.2.0` — set versi lalu broadcast\n" +
		"• `!jpmupdate changelog` — set catatan perubahan lalu broadcast",

	"contributor": "🏆 *Kontributor Bot*\n\n" +
		"• `!contributor` — lihat daftar kontributor\n" +
		"• Alias: `!tqto`, `!thanks`",

	"ai": "🤖 *Cara pakai* `!ai` (khusus owner):\n\n" +
		"• `!ai on` — nyalakan AI chat\n" +
		"• `!ai off` — matikan\n" +
		"• `!ai list` — daftar session percakapan\n" +
		"• `!ai load 628xxx` — lanjutkan session user itu\n" +
		"• `!ai new` — mulai percakapan baru\n\n" +
		"*Cara pakai (semua user):*\n" +
		"• Ketik `yuuki <pertanyaan>` atau `yuki <pertanyaan>`\n" +
		"• Contoh: `yuuki apa kabar?`",

	"ss": "🔧 *Cara pakai* `!ss` (Screenshot Web):\n\n" +
		"• `!ss https://google.com` — screenshot desktop\n" +
		"• `!ss https://wa.me mobile` — screenshot mobile\n" +
		"• Device: `desktop` (default), `mobile`, `tablet`",

	"qr": "🔧 *Cara pakai* `!qr` (QR Code):\n\n" +
		"• `!qr https://wa.me/628xxx`\n" +
		"• `!qr teks apapun`",

	"tr": "🔧 *Cara pakai* `!tr` (Translate):\n\n" +
		"• `!tr en Halo semua` → Hello everyone\n" +
		"• `!tr id Hello world` → Halo dunia\n" +
		"• `!tr ja Apa kabar` → お元気ですか\n" +
		"• Atau reply teks: `!tr <kode_bahasa>`\n\n" +
		"Kode: `id` `en` `ja` `ko` `ar` `zh` dll",

	"bypass": "🔧 *Cara pakai* `!bypass`:\n\n" +
		"• Paste URL yang mau di-bypass:\n  `!bypass https://linkvertise.com/xxx`\n\n" +
		"• Mendukung: Linkvertise, Shorte.st, dll.",

	"adds": "🤖 *Cara pakai* `!adds`:\n\n" +
		"• Format nomor lengkap:\n  `!adds 6285xxxxxxxxx`\n\n" +
		"• Bot kirim kode pairing → masuk di WA target:\n  _Perangkat Tertaut → Tautkan dengan Nomor_",

	"cancels": "🤖 *Cara pakai* `!cancels`:\n\n" +
		"• Batalkan pairing sender:\n  `!cancels sender2`\n\n" +
		"• Cek nama sender via `!ls`",

	"lanjut": "📞 *Cara pakai* `!lanjut`:\n\n" +
		"• Resume jika lagu stuck (tanpa judul):\n  `!lanjut`\n\n" +
		"• Tambah lagu ke call aktif:\n  `!lanjut Satu Rasa Cinta`",

	"del": "🗑️ *Cara pakai* `!del`:\n\n" +
		"• Hapus lagu dari antrian (nama/sebagian):\n  `!del Bertaut`\n\n" +
		"• Cek antrian dulu via `!antri`",

	"prank": "😹 *Cara pakai* `!prank`:\n\n" +
		"• Reply pesan audio/voice note:\n  _reply audio → `!prank sender1`_\n\n" +
		"• Audio prank disisipkan ke call yang sedang berjalan.",

	"gstatus": "📡 *Cara pakai* `!gstatus`:\n\n" +
		"• Reply media/teks dulu, lalu ketik:\n  _reply gambar → `!gstatus`_\n\n" +
		"• Bot tampilkan daftar grup → pilih nomor\n" +
		"• Bot harus jadi *admin* grup untuk posting ke tab Updates.",

	"kirim": "📡 *Cara pakai* `!kirim`:\n\n" +
		"• Reply media/teks dulu, lalu ketik:\n  _reply gambar → `!kirim`_\n\n" +
		"• Bot tampilkan daftar saluran → pilih nomor\n" +
		"• Untuk kirim ke saluran default: `!upch`",

	"upch": "📡 *Cara pakai* `!upch`:\n\n" +
		"`!upch` memposting konten sebagai *status/update di saluran WA*\n" +
		"(muncul di tab Updates saluran, seperti story tapi di channel)\n\n" +
		"• Posting gambar/video ke saluran:\n  _reply media → `!upch`_\n\n" +
		"• Posting teks ke saluran:\n  `!upch Halo semua!`",

	"ap": "👑 *Cara pakai* `!ap` (add premium):\n\n" +
		"• `!ap 6285xxxxxxxxx`",

	"dp": "👑 *Cara pakai* `!dp` (del premium):\n\n" +
		"• `!dp 6285xxxxxxxxx`",

	"ao": "👑 *Cara pakai* `!ao` (add owner):\n\n" +
		"• `!ao 6285xxxxxxxxx`",

	"do": "👑 *Cara pakai* `!do` (del owner):\n\n" +
		"• `!do 6285xxxxxxxxx`",

	"tenor": "🎨 *Cara pakai* `!tenor` (Sticker):\n\n" +
		"• Cari sticker by kata kunci:\n  `!tenor kucing lucu`\n\n" +
		"• Bot kirim hingga 5 sticker Tenor.",

	"brat": "🎨 *Cara pakai* `!brat`:\n\n" +
		"• `!brat teks kamu`\n" +
		"• Bisa juga reply pesan → `!brat`",

	"bratvid": "🎨 *Cara pakai* `!bratvid`:\n\n" +
		"• `!bratvid teks kamu`\n" +
		"• Bot kirim sticker animasi ala brat",

	"smeme": "🎨 *Cara pakai* `!smeme` (Sticker Meme):\n\n" +
		"• Reply gambar → `!smeme teks atas|teks bawah`\n" +
		"• Bot buat meme dari gambar kamu",

	"qc": "🎨 *Cara pakai* `!qc` (Quote Card):\n\n" +
		"• `!qc teks quote kamu`\n" +
		"• Bisa juga reply pesan → `!qc`\n" +
		"• Bot buat kartu quote pakai foto profilmu",

	"iqc": "📱 *Cara pakai* `!iqc` (iPhone Chat):\n\n" +
		"• `!iqc Halo, apa kabar?`\n" +
		"• Bisa juga reply pesan → `!iqc`",

	"tohitam": "✨ *Cara pakai efek AI gambar*:\n\n" +
		"• Reply gambar → ketik salah satu command:\n\n" +
		"`tohitam` `toputih` `tozombie` `toroblox`\n" +
		"`tomirror` `tochibi` `toghibli` `tojapanese`\n" +
		"`tojepang` `tolego` `toreal` `totua`\n" +
		"`tomoai` `tomonyet` `topacar` `toroh`\n" +
		"`totato` `toviking` `tobotak` `tofunk`\n" +
		"`tofigura` `tohijab` `tokacamata` `tokamboja`\n" +
		"`toliquor` `tomaid` `topeci` `topiramida`\n" +
		"`tounderground`",

	// "hdvid": "🎬 *Cara pakai* `!hdvid` (HD Video):\n\n" + // ⚠️ DISABLED sementara — ffmpeg berat
	// 	"• Reply video → `!hdvid`\n" +
	// 	"• Video kecil di-upscale ke 1080p, video besar di-sharpen",

	"fetch": "🌐 *Cara pakai* `!fetch`:\n\n" +
		"• Ambil isi URL (JSON/teks):\n  `!fetch https://api.example.com/data`",

	"uploadgh": "📤 *Cara pakai* `!uploadgh`:\n\n" +
		"• Reply media → `!uploadgh`\n" +
		"• Opsional nama file: `!uploadgh foto.png`\n" +
		"• Bot upload ke repo GitHub bot",

	"source": "🔍 *Cara pakai* `!source`:\n\n" +
		"• Lihat source HTML website:\n  `!source https://example.com`",

	"enc": "🔐 *Cara pakai* `!enc` (Encode Base64):\n\n" +
		"• `!enc teks rahasia`\n" +
		"• Bisa juga reply teks → `!enc`",

	"delmsg": "🗑️ *Cara pakai* `!delmsg`:\n\n" +
		"• Reply pesan yang mau dihapus → `!delmsg`\n" +
		"• Bot hapus pesan tersebut dari chat",

	"codesnap": "📸 *Cara pakai* `!codesnap`:\n\n" +
		"• Ubah kode jadi screenshot:\n  `!codesnap const x = 42`\n" +
		"• Multi-baris bisa pakai enter",

	"sai": "🖼️ *Cara pakai* `!sai` (Sticker AI):\n\n" +
		"• *Reply sticker biasa* → `!sai` → jadi sticker AI (ada label AI)\n" +
		"• *Reply/kirim gambar atau video* → `!sai`\n" +
		"• Opsi (gambar/video):\n" +
		"  `--crop` crop kotak\n" +
		"  `--resize WxH` resize\n" +
		"  `--circle` lingkaran\n" +
		"  `--rounded` sudut melengkung\n\n" +
		"• Contoh: `!sai --circle`, `!sai NamaPack Author`\n" +
		"• Video maksimal 10 detik",

	"douyin": "✨ *Cara pakai* `!douyin`:\n\n" +
		"• Download video Douyin:\n  `!douyin https://v.douyin.com/xxxxx`",

	"menucat": "🗂️ *Cara pakai* `!menucat`:\n\n" +
		"• Lihat command per kategori:\n  `!menucat tools`\n" +
		"• Tanpa argumen → daftar semua kategori",

	"gempa": "🌊 *Cara pakai* `!gempa`:\n\n" +
		"• Info gempa terkini dari BMKG:\n  `!gempa`\n" +
		"• Termasuk peta shakemap (kalau tersedia)",

	"ipinfo": "🌐 *Cara pakai* `!ipinfo`:\n\n" +
		"• Lookup info sebuah IP:\n  `!ipinfo 8.8.8.8`\n" +
		"• Hasil: lokasi, ISP, timezone, dll",

	"rate": "⭐ *Cara pakai* `!rate`:\n\n" +
		"• Minta bot memberi rating:\n  `!rate aku ganteng`\n" +
		"• Hasilnya random & cuma buat seru-seruan",

	"cekkhodam": "🔮 *Cara pakai* `!cekkhodam`:\n\n" +
		"• Cek khodam kamu:\n  `!cekkhodam`\n" +
		"• Hasilnya random & cuma buat seru-seruan",

	"quran": "📖 *Cara pakai* `!quran`:\n\n" +
		"• Baca ayat Al-Quran:\n  `!quran 1` (surah 1)\n" +
		"• Baca ayat tertentu:\n  `!quran 1:1-7` (surah:ayat)\n" +
		"• Maksimal 10 ayat per permintaan",

	"githubstalk": "🐙 *Cara pakai* `!githubstalk`:\n\n" +
		"• Stalk akun GitHub:\n  `!githubstalk torvalds`\n" +
		"• Hasil: bio, followers, repos, dll",
}

// buildMenuSections — section dropdown "Pilih Command" (V3) / kartu utama (V4).
// DISUSUN MANUAL (bukan dari registry) supaya bisa:
//   - row "langsung eksekusi" (id = perintah) vs row "butuh input manual"
//     (id = howto — klik → tampilkan panduan, karena perintah butuh input);
//   - kontrol tampilan per level user (public/owner/creator).
func buildMenuSections(ownerSender, ownerLevel, premiumSender bool) []listSection {
	p := Prefix
	// Shortcut: buat row "langsung eksekusi"
	do := func(cat, title, desc, cmd string) listRow {
		return listRow{cat, title, desc, p + cmd}
	}
	// Shortcut: buat row "butuh input manual" — klik tampilkan panduan
	how := func(cat, title, desc, cmd string) listRow {
		return listRow{cat, title, desc + " (ketik manual)", howtoPrefix + cmd}
	}

	sections := []listSection{
		{
			title: "Umum",
			rows: []listRow{
				do("Umum", "Ping", "Cek latency & status", "ping"),
				do("Umum", "Info", "Info pesan saat ini", "info"),
				do("Umum", "My JID", "Lihat JID kamu", "myjid"),
				do("Umum", "Owner", "Cek status kamu", "owner"),
				do("Umum", "Sticker", "Reply media → sticker", "sticker"),
				do("Umum", "Donasi", "QRIS donasi bot", "donasi"),
				do("Umum", "Contributor", "Lihat kontributor bot", "contributor"),
				do("Umum", "All Menu", "Daftar semua command (teks)", "allmenu"),
				do("Umum", "Menu Kategori", "Lihat command per kategori", "menucat"),
			},
		},
		listSection{
			title: "Fun",
			rows: []listRow{
				do("Fun", "Gempa", "Info gempa terkini BMKG", "gempa"),
				how("Fun", "IP Info", "Lookup info sebuah IP", "ipinfo"),
				how("Fun", "Rate", "Minta bot memberi rating", "rate"),
				do("Fun", "Cek Khodam", "Cek khodam kamu", "cekkhodam"),
				how("Fun", "Quran", "Baca ayat Al-Quran", "quran"),
				how("Fun", "GitHub Stalk", "Stalk akun GitHub", "githubstalk"),
			},
		},
		listSection{
			title: "Grup (Kelola)",
			rows: []listRow{
				do("Grup", "Kunci/Buka", "Kunci atau buka grup", "close"),
				how("Grup", "Tambah Member", "Tambah member via nomor (multi)", "add"),
				how("Grup", "Kick Member", "Keluarkan member (multi)", "kick"),
				how("Grup", "Naik/Turun Admin", "Promote/demote admin", "promote"),
				do("Grup", "Tag All", "Tag semua member", "tagall"),
				how("Grup", "Hide Tag", "Pesan + tag semua member", "hidetag"),
				how("Grup", "Set Nama", "Ganti nama grup", "setname"),
				how("Grup", "Set Deskripsi", "Ganti deskripsi grup", "setdesc"),
				how("Grup", "Set Foto", "Ganti foto grup (reply gambar)", "setppgc"),
				do("Grup", "Link Grup", "Ambil link undangan grup", "linkgc"),
				do("Grup", "Reset Link", "Reset link undangan grup", "revoke"),
				do("Grup", "Info Grup", "Info grup (nama/id/owner/member)", "infogc"),
				how("Grup", "Warn", "Warn member (3x = kick)", "warn"),
				do("Grup", "Warn List", "Daftar warn semua member", "warnlist"),
				how("Grup", "Reset Warn", "Reset warn member/grup", "resetwarn"),
				do("Grup", "Bot Keluar", "Bot keluar dari grup", "out"),
			},
		},
		listSection{
			title: "Grup (Jaga)",
			rows: []listRow{
				do("Grup", "Jaga", "Status jaga grup (antilink/antitoxic/welcome)", "jaga"),
				do("Grup", "Anti Link", "Peringatkan pesan ber-link", "antilink"),
				do("Grup", "Anti Toxic", "Peringatkan kata kasar", "antitoxic"),
				do("Grup", "Welcome", "Sambut member baru", "welcome"),
				how("Grup", "Set Welcome", "Pesan welcome custom (@user/@group)", "setwelcome"),
			},
		},
	}
	if !ownerSender && !premiumSender {
		return sections
	}
	// Premium: hanya section Audio Call dengan Play (kontrol lain owner-only).
	if premiumSender && !ownerSender {
		return append(sections, listSection{
			title: "Audio Call",
			rows: []listRow{
				how("Audio", "Play", "Putar lagu ke target", "play"),
			},
		})
	}

	sections = append(sections,
		listSection{
			title: "Audio Call",
			rows: []listRow{
				how("Audio", "Play", "Putar lagu ke target", "play"),
				how("Audio", "Lanjut", "Tambah/lanjut lagu", "lanjut"),
				do("Audio", "Skip", "Skip lagu sekarang", "sk"),
				do("Audio", "Stop Call", "Hentikan call", "stop"),
				do("Audio", "Antri", "Lihat antrian lagu", "antri"),
				how("Audio", "Hapus Antrian", "Hapus lagu dari antrian", "del"),
				how("Audio", "Prank", "Sisipin audio ke call", "prank"),
			},
		},
		listSection{
			title: "Video Call",
			rows: []listRow{
				how("Video", "Play Video", "Video call YouTube", "video"),
				do("Video", "Skip Video", "Skip video sekarang", "skv"),
				do("Video", "Stop Video", "Hentikan video call", "sv"),
				do("Video", "Antrian Video", "Lihat antrian video", "qv"),
			},
		},
		listSection{
			title: "Sender",
			rows: []listRow{
				do("Sender", "List Sender", "Status semua sender", "ls"),
				how("Sender", "Add Sender", "Tambah akun penelpon", "adds"),
				how("Sender", "Cancel Add", "Batalkan pairing", "cancels"),
			},
		},
		listSection{
			title: "Saluran WA",
			rows: []listRow{
				how("Saluran", "Post Status Saluran", "Reply media → posting ke status/updates saluran", "upch"),
				how("Saluran", "Kirim ke Saluran Lain", "Reply media → pilih saluran tujuan", "kirim"),
				do("Saluran", "List Saluran", "Lihat semua saluran yang diikuti bot", "saluran"),
				how("Saluran", "Group Status", "Reply media → kirim ke tab Updates grup", "gstatus"),
			},
		},
		listSection{
			title: "Downloader",
			rows: []listRow{
				how("DL", "TikTok", "Download video/foto TikTok", "tt"),
				how("DL", "Douyin", "Download video Douyin", "douyin"),
				how("DL", "YouTube MP3", "Download audio dari YouTube", "ytmp3"),
				how("DL", "YouTube Play", "YouTube → video + audio", "ytplay"),
				how("DL", "Instagram", "Download foto/video Instagram", "ig"),
				how("DL", "Facebook", "Download video Facebook", "fb"),
				how("DL", "SoundCloud", "Download audio SoundCloud", "soundcloud"),
			},
		},
		listSection{
			title: "Search",
			rows: []listRow{
				how("Search", "YouTube", "Cari video YouTube", "yts"),
				how("Search", "Pinterest", "Cari gambar Pinterest", "pinterest"),
				how("Search", "Bing Images", "Cari gambar Bing", "bingimage"),
				how("Search", "Apple Music", "Cari lagu Apple Music", "applemusic"),
				how("Search", "Wikipedia", "Cari artikel Wikipedia", "wiki"),
				how("Search", "KBBI", "Cari definisi kata", "kbbi"),
				how("Search", "Lyrics", "Cari lirik lagu", "lyrics"),
			},
		},
		listSection{
			title: "Tools",
			rows: []listRow{
				how("Tools", "Translate", "Terjemahkan teks", "tr"),
				how("Tools", "Screenshot", "Screenshot website", "ss"),
				how("Tools", "QR Code", "Buat QR code dari teks", "qr"),
				how("Tools", "Barcode", "Buat barcode Code-128", "barcode"),
				how("Tools", "Bypass Link", "Bypass URL shortener", "bypass"),
				how("Tools", "Temp Email", "Buat email sementara", "tempmail"),
				how("Tools", "What Music", "Identifikasi lagu dari audio", "whatmusic"),
				how("Tools", "ID Saluran", "Cek ID channel WA", "idch"),
				how("Tools", "ID Grup", "Cek ID grup WA", "idgc"),
				do("Tools", "Kode Bahasa", "Daftar kode bahasa untuk !tr", "kodebahasa"),
			},
		},
		listSection{
			title: "Tools Lain",
			rows: []listRow{
				how("Tools", "Fetch URL", "Ambil isi URL (JSON/teks)", "fetch"),
				how("Tools", "Upload GitHub", "Reply media → upload ke GitHub", "uploadgh"),
				how("Tools", "Source Web", "Lihat source HTML website", "source"),
				how("Tools", "Encode Base64", "Encode teks ke Base64", "enc"),
				how("Tools", "Delete Pesan", "Reply pesan → hapus", "delmsg"),
			},
		},
		listSection{
			title: "Image Tools",
			rows: []listRow{
				do("Image", "HD Image", "Perjelas gambar 2x", "hd"),
				// do("Image", "HD Video", "Perjelas video", "hdvid"), // ⚠️ DISABLED sementara — ffmpeg berat
				do("Image", "Remove BG", "Hapus background gambar", "removebg"),
				do("Image", "Blur", "Blur gambar", "blur"),
				how("Image", "Caption", "Tambah caption ke gambar", "cap"),
				do("Image", "Wanted", "Buat wanted poster", "wanted"),
				how("Image", "Compress", "Kompres gambar atau video", "compress"),
			},
		},
		listSection{
			title: "Sticker & Maker",
			rows: []listRow{
				how("Maker", "Tenor", "Cari sticker by kata kunci", "tenor"),
				how("Maker", "Brat", "Buat sticker brat dari teks", "brat"),
				how("Maker", "Brat Video", "Buat sticker animasi brat", "bratvid"),
				how("Maker", "SMeme", "Reply gambar → meme sticker", "smeme"),
				how("Maker", "Quote Card", "Buat quote card dari teks", "qc"),
				how("Maker", "iPhone Chat", "Buat fake chat iPhone", "iqc"),
				how("Maker", "CodeSnap", "Screenshot kode → gambar", "codesnap"),
				how("Maker", "Sai", "Jadikan sticker jadi sticker AI", "sai"),
			},
		},
		listSection{
			title: "Efek AI",
			rows: []listRow{
				how("Efek", "Efek AI Gambar", "29 efek: tohitam, toputih, toghibli, dll", "tohitam"),
			},
		},
		listSection{
			title: "Alight Motion",
			rows: []listRow{
				how("Alight", "Bulk Akun", "Buat akun Alight Motion premium (max 1)", "bulk"),
				how("Alight", "Kirim Link", "Kirim magic link ke email", "am-send"),
				how("Alight", "Aktivasi", "Aktivasi akun pakai magic link", "am-aktif"),
			},
		},
		listSection{
			title: "Konversi",
			rows: []listRow{
				do("Konversi", "ToMP3", "Reply video/audio → MP3", "tomp3"),
				do("Konversi", "ToImg", "Reply sticker → Gambar PNG", "toimg"),
				do("Konversi", "ToGIF/MP4", "Reply sticker animasi → Video", "togif"),
				do("Konversi", "ToURL", "Reply media → dapat link URL", "tourl"),
				how("Konversi", "ToFile", "Reply teks → file download", "tofile"),
				do("Konversi", "Resend", "Kirim ulang media tanpa kompres", "resend"),
				do("Konversi", "Read ViewOnce", "Baca pesan view-once", "rvo"),
			},
		},
		listSection{
			title: "Pengaturan",
			rows: []listRow{
				do("Mode", "Self", "Bot hanya respon owner", "self"),
				do("Mode", "Public", "Buka akses umum", "public"),
				do("Mode", "Set Menu", "Ganti versi menu", "setmenu"),
			},
		},
	)
	if ownerLevel {
		sections = append(sections,
			listSection{
				title: "JPM Broadcast",
				rows: []listRow{
					do("JPM", "JPM", "Broadcast pesan ke semua grup (menu)", "jpm"),
					how("JPM", "JPM HideTag", "Broadcast + tag semua member", "jpmht"),
					how("JPM", "JPM Channel", "Broadcast ke semua saluran", "jpmch"),
					how("JPM", "Auto JPM", "Auto-broadcast terjadwal", "autojpm"),
					do("JPM", "Stop JPM", "Hentikan broadcast yang berjalan", "stopjpm"),
					how("JPM", "Set Delay", "Atur jeda antar grup (1-30 detik)", "setdelayjpm"),
					how("JPM", "BL JPM", "Blacklist grup dari JPM", "bljpm"),
					how("JPM", "BL Auto JPM", "Blacklist grup dari auto-JPM", "blautojpm"),
					how("JPM", "Update JPM", "Broadcast info update bot", "jpmupdate"),
				},
			},
			listSection{
				title: "Owner",
				rows: []listRow{
					how("Owner", "Add Prem", "Tambah user premium", "ap"),
					how("Owner", "Del Prem", "Hapus user premium", "dp"),
					how("Owner", "Add Owner", "Tambah owner", "ao"),
					how("Owner", "Del Owner", "Hapus owner", "do"),
					how("Owner", "Blacklist", "Blacklist user (add/del/list)", "bl"),
					how("Owner", "Clear Cache", "Bersihkan cache file temp", "clear"),
				},
			})
	}
	return sections
}

func sendMenuV3(ctx context.Context, chat types.JID, evt *events.Message, ownerSender, ownerLevel, premiumSender bool) {
	p := Prefix
	sections := buildMenuSections(ownerSender, ownerLevel, premiumSender)
	bodyText := menuGreeting(evt, ownerSender, ownerLevel, premiumSender) +
		menuText(ownerSender, ownerLevel, premiumSender)

	// ── Coba kirim InteractiveMessage + single_select (builder) ─────────────
	// Select list + tombol quick_reply TQTO bersebelahan (native flow mendukung
	// beberapa tombol dalam satu pesan).
	b := NewMsgBuilder().
		SetHeader(BotName, fmt.Sprintf("by %s  •  prefix: %s", BotDeveloper, p)).
		SetImageHeader(getMenuImage(ctx)).
		SetBody(bodyText).
		SetFooter(channelFooter()).
		SetContextInfo(menuCtxInfo(ctx, evt)).
		AddSelectButton("Pilih Command", sections).
		AddQRButton("🏆 TQTO", p+"contributor")

	pool.logger.Debug().Str("chat", chat.String()).Msg("sendMenuV3: mencoba InteractiveMessage single_select + biz node")
	if err := b.Send(ctx, chat); err == nil {
		pool.logger.Debug().Msg("sendMenuV3: berhasil dikirim")
		return
	} else {
		pool.logger.Warn().Err(err).Msg("sendMenuV3: InteractiveMessage gagal, coba ListMessage")
	}

	// ── Fallback: ListMessage ──────────────────────────────────────────────
	var lmSections []*waE2E.ListMessage_Section
	for _, sec := range sections {
		var rows []*waE2E.ListMessage_Row
		for _, r := range sec.rows {
			rows = append(rows, &waE2E.ListMessage_Row{
				RowID:       proto.String(r.id),
				Title:       proto.String(r.title),
				Description: proto.String(r.desc),
			})
		}
		lmSections = append(lmSections, &waE2E.ListMessage_Section{
			Title: proto.String(sec.title),
			Rows:  rows,
		})
	}
	listMsg := &waE2E.Message{
		ListMessage: &waE2E.ListMessage{
			Title:       proto.String(fmt.Sprintf("%s", BotName)),
			Description: proto.String(bodyText),
			ButtonText:  proto.String("Pilih Command"),
			FooterText:  proto.String(fmt.Sprintf("by %s  •  prefix: %s", BotDeveloper, p)),
			ListType:    waE2E.ListMessage_SINGLE_SELECT.Enum(),
			Sections:    lmSections,
		},
	}
	_, err := waClient.SendMessage(ctx, chat, listMsg)
	if err == nil {
		pool.logger.Info().Msg("sendMenuV3: ListMessage berhasil dikirim")
		return
	}
	pool.logger.Warn().Err(err).Msg("sendMenuV3: ListMessage gagal, fallback plain text")
	sendText(ctx, chat, bodyText)
}

// ─── !setmenu — ganti versi menu saat runtime ────────────────────────────────

func handleSetMenu(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	v := -1
	switch strings.TrimSpace(args) {
	case "0":
		v = 0
	case "1":
		v = 1
	case "2":
		v = 2
	case "3":
		v = 3
	}
	if v < 0 {
		cur := getMenuVersion()
		sendText(ctx, chat, fmt.Sprintf(
			"*Menu Version* sekarang: *V%d*\n\n"+
				"Ganti dengan:\n"+
				"• *%ssetmenu 0* — Plain text _(paling kompatibel)_\n"+
				"• *%ssetmenu 1* — Interactive + quick reply buttons\n"+
				"• *%ssetmenu 2* — Interactive ringkas\n"+
				"• *%ssetmenu 3* — Single Select list _(default, paling rapi)_\n",
			cur, Prefix, Prefix, Prefix, Prefix))
		return
	}
	setMenuVersion(v)
	labels := map[int]string{
		0: "Plain text",
		1: "Interactive V1 (quick reply buttons)",
		2: "Interactive V2 (ringkas)",
		3: "Single Select List — paling rapi",
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("Menu diubah ke *V%d* — %s", v, labels[v]))
	pool.logger.Info().Int("version", v).Msg("menu version diubah")
}

// ─── Handle button click ──────────────────────────────────────────────────────
// Saat user klik tombol interaktif, WA kirim InteractiveResponseMessage.
// Kita ekstrak button ID dan perlakukan sebagai teks command biasa.
// Port dari menu2.js handler: msg.__overrideBody = buttonId

// extractButtonCommand mengekstrak command dari semua tipe button response.
// Port dari getMessageText() di index.js — handle semua format WhatsApp button.
func extractButtonCommand(evt *events.Message) string {
	msg := evt.Message

	// 1. InteractiveResponseMessage — quick_reply dan single_select (V1/V2/V3)
	if ir := msg.GetInteractiveResponseMessage(); ir != nil {
		if nf := ir.GetNativeFlowResponseMessage(); nf != nil {
			raw := nf.GetParamsJSON()
			if raw != "" {
				// Parse JSON params — bisa berisi "id", "buttonId", atau "selectedRowId"
				var params map[string]json.RawMessage
				if err := json.Unmarshal([]byte(raw), &params); err == nil {
					for _, key := range []string{"id", "selectedRowId", "buttonId"} {
						if v, ok := params[key]; ok {
							var s string
							if err := json.Unmarshal(v, &s); err == nil && s != "" {
								return s
							}
						}
					}
				}
			}
		}
	}

	// 2. ListResponseMessage — dari ListMessage single_select (V3 fallback)
	if lr := msg.GetListResponseMessage(); lr != nil {
		if id := lr.GetSingleSelectReply().GetSelectedRowID(); id != "" {
			return id
		}
		if title := lr.GetTitle(); title != "" {
			return title
		}
	}

	// 3. ButtonsResponseMessage — dari ButtonsMessage (V1/V2 alternatif)
	if br := msg.GetButtonsResponseMessage(); br != nil {
		if id := br.GetSelectedButtonID(); id != "" {
			return id
		}
	}

	// 4. TemplateButtonReplyMessage
	if tb := msg.GetTemplateButtonReplyMessage(); tb != nil {
		return tb.GetSelectedID()
	}

	return ""
}
