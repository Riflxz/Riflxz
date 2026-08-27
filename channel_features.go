package main

// channel_features.go — fitur kirim media/teks ke saluran WhatsApp (newsletter).
//
// Command:
//   m!listsaluran            → tampilkan semua saluran yang diikuti bot
//   m!kirimch                → reply media + command → pilih saluran → kirim
//   m!kirimch <N>            → kalau sudah ada pending, kirim ke saluran nomor N
//   m!kirimch <nama saluran> → cari saluran, kalau 1 hasil langsung kirim
//
// Newsletter butuh UploadNewsletter (unencrypted) + SendRequestExtra{MediaHandle}.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// ─── Channel entry & pending state ───────────────────────────────────────────

type channelEntry struct {
	JID         types.JID
	Name        string
	Subscribers int
	Role        string // "owner" | "admin" | "subscriber" | "guest"
}

type pendingChannelSend struct {
	channels  []channelEntry
	media     *detectedMedia
	caption   string
	expiresAt time.Time
	// Fix: anti race — dua pesan user dalam jarak dekat bisa baca pending yang
	// sama sebelum salah satunya sempat delete dari map → kirim double.
	mu      sync.Mutex
	claimed bool
}

var (
	chPendingMu sync.Mutex
	chPending   = map[string]*pendingChannelSend{} // key = sender number
)

// ─── Fetch saluran ────────────────────────────────────────────────────────────

func fetchChannelList(ctx context.Context) ([]channelEntry, error) {
	newsletters, err := waClient.GetSubscribedNewsletters(ctx)
	if err != nil {
		return nil, err
	}
	var list []channelEntry
	for _, n := range newsletters {
		role := ""
		if n.ViewerMeta != nil {
			role = string(n.ViewerMeta.Role)
		}
		list = append(list, channelEntry{
			JID:         n.ID,
			Name:        n.ThreadMeta.Name.Text,
			Subscribers: n.ThreadMeta.SubscriberCount,
			Role:        role,
		})
	}
	// Urutkan: owner/admin duluan, lalu alphabetical
	sort.Slice(list, func(i, j int) bool {
		ri, rj := roleWeight(list[i].Role), roleWeight(list[j].Role)
		if ri != rj {
			return ri > rj
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list, nil
}

func roleWeight(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	case "subscriber":
		return 1
	default:
		return 0
	}
}

func filterChannels(list []channelEntry, query string) []channelEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []channelEntry
	for _, c := range list {
		if strings.Contains(strings.ToLower(c.Name), q) {
			out = append(out, c)
		}
	}
	return out
}

func isNumericStr(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(s) > 0
}

func cleanExpiredChPending() {
	chPendingMu.Lock()
	defer chPendingMu.Unlock()
	now := time.Now()
	for k, v := range chPending {
		if now.After(v.expiresAt) {
			delete(chPending, k)
		}
	}
}

// ─── m!listsaluran ────────────────────────────────────────────────────────────

func handleListSaluran(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	reactMsg(ctx, evt, "⏳")

	channels, err := fetchChannelList(ctx)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar saluran: "+err.Error())
		return
	}
	if len(channels) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot tidak mengikuti saluran manapun.")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "╔═ 『 📡 DAFTAR SALURAN 』\n")
	fmt.Fprintf(&b, "║ Total: *%d saluran*\n", len(channels))
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	for i, c := range channels {
		roleLabel := roleEmoji(c.Role)
		fmt.Fprintf(&b, "║ *[%d]* %s %s\n", i, roleLabel, c.Name)
		fmt.Fprintf(&b, "║      👥 %d subscriber\n", c.Subscribers)
	}
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	fmt.Fprintf(&b, "║ Kirim ke saluran: *%skirimch*\n", Prefix)
	fmt.Fprintf(&b, "╚══════════════════════════")

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, b.String())
}

func roleEmoji(role string) string {
	switch role {
	case "owner":
		return "👑"
	case "admin":
		return "🔧"
	case "subscriber":
		return "📻"
	default:
		return "👤"
	}
}

// ─── m!kirimch ────────────────────────────────────────────────────────────────

// handleKirimCh — args datang dari router (sama seperti handleSWGC2): getArgs
// membaca protobuf Conversation/ExtendedText yang KOSONG pada pesan hasil klik
// tombol (InteractiveResponseMessage) → pending tidak ketemu.
func handleKirimCh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	user := senderUser(evt)
	cleanExpiredChPending()

	// ── Cek pending state ─────────────────────────────────────────────────────
	chPendingMu.Lock()
	pending, hasPending := chPending[user]
	chPendingMu.Unlock()

	if hasPending && time.Now().After(pending.expiresAt) {
		chPendingMu.Lock()
		delete(chPending, user)
		chPendingMu.Unlock()
		hasPending = false
		sendText(ctx, chat, "⌛ Sesi kadaluarsa. Ulangi *"+Prefix+"kirimch*.")
	}

	if hasPending && args != "" {
		if isNumericStr(args) {
			// Fix: validasi hasil Atoi — isNumericStr bisa lolos buat angka
			// raksasa (>int64), Atoi error → idx=0 yang salah sasaran.
			idx, err := strconv.Atoi(args)
			if err != nil || idx < 0 || idx >= len(pending.channels) {
				sendText(ctx, chat, fmt.Sprintf("❌ Pilih nomor 0–%d.", len(pending.channels)-1))
				return
			}
			sendToChannel(ctx, evt, user, pending, pending.channels[idx])
			return
		}
		filtered := filterChannels(pending.channels, args)
		if len(filtered) == 1 {
			sendToChannel(ctx, evt, user, pending, filtered[0])
			return
		} else if len(filtered) > 1 {
			pending.channels = filtered
			pending.expiresAt = time.Now().Add(3 * time.Minute)
			chPendingMu.Lock()
			chPending[user] = pending
			chPendingMu.Unlock()
			sendChannelList(ctx, chat, filtered, fmt.Sprintf("🔍 %d saluran cocok. Pilih:", len(filtered)), "kirimch")
			return
		}
		sendText(ctx, chat, fmt.Sprintf("❌ Saluran *%s* tidak ditemukan.", args))
		return
	}

	// ── Mulai flow baru ───────────────────────────────────────────────────────
	reactMsg(ctx, evt, "⏳")

	med, err := detectMedia(ctx, evt)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	if med == nil && args == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Reply media atau tulis teks dulu.\n\n"+
				"*Cara pakai:*\n"+
				"① Reply gambar/video/audio → *%skirimch*\n"+
				"② Atau: *%skirimch <teks>*\n\n"+
				"Bot tampilkan daftar saluran, lalu pilih nomor atau nama.",
			Prefix, Prefix))
		return
	}
	if med != nil && args != "" {
		med.caption = args
	}

	allChannels, err := fetchChannelList(ctx)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar saluran: "+err.Error())
		return
	}
	if len(allChannels) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot tidak mengikuti saluran manapun.")
		return
	}

	// Filter hanya yang bisa kirim (owner atau admin)
	var writeable []channelEntry
	for _, c := range allChannels {
		if c.Role == "owner" || c.Role == "admin" {
			writeable = append(writeable, c)
		}
	}
	if len(writeable) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot bukan admin/owner di saluran manapun.\n"+
			"Bot harus jadi admin saluran untuk bisa kirim.")
		return
	}

	newPending := &pendingChannelSend{
		channels:  writeable,
		media:     med,
		caption:   args,
		expiresAt: time.Now().Add(3 * time.Minute),
	}

	displayChannels := writeable
	header := fmt.Sprintf("📡 *%d saluran* (admin/owner) — pilih:", len(writeable))

	// Kalau args = nama, langsung filter
	if args != "" && !isNumericStr(args) {
		filtered := filterChannels(writeable, args)
		if len(filtered) == 1 {
			chPendingMu.Lock()
			chPending[user] = newPending
			chPendingMu.Unlock()
			sendToChannel(ctx, evt, user, newPending, filtered[0])
			return
		} else if len(filtered) > 0 {
			displayChannels = filtered
			newPending.channels = filtered
			header = fmt.Sprintf("🔍 *%d saluran* cocok:", len(filtered))
		}
	}

	// Kalau hanya 1 saluran yang bisa kirim, langsung pilih
	if len(writeable) == 1 {
		chPendingMu.Lock()
		chPending[user] = newPending
		chPendingMu.Unlock()
		sendToChannel(ctx, evt, user, newPending, writeable[0])
		return
	}

	chPendingMu.Lock()
	chPending[user] = newPending
	chPendingMu.Unlock()

	reactMsg(ctx, evt, "✅")
	sendChannelList(ctx, chat, displayChannels, header, "kirimch")
}

// ─── Kirim ke saluran ─────────────────────────────────────────────────────────

func sendToChannel(ctx context.Context, evt *events.Message, user string, p *pendingChannelSend, target channelEntry) {
	chat := evt.Info.Chat

	chPendingMu.Lock()
	delete(chPending, user)
	chPendingMu.Unlock()

	// Claim anti-double-send (lihat komentar di struct).
	p.mu.Lock()
	if p.claimed {
		p.mu.Unlock()
		return
	}
	p.claimed = true
	p.mu.Unlock()

	if target.Role != "owner" && target.Role != "admin" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Bot bukan admin di saluran *%s* (role: %s).\n"+
				"Harus owner/admin untuk bisa kirim ke saluran.",
			target.Name, target.Role))
		return
	}

	reactMsg(ctx, evt, "⏳")
	typeLabel := "teks"
	if p.media != nil {
		typeLabel = p.media.mediaType
	}
	sendText(ctx, chat, fmt.Sprintf("⏳ Mengirim %s ke saluran *%s*...", typeLabel, target.Name))

	var err error
	if p.media == nil {
		// Teks — newsletter tidak perlu upload; ExtendedTextMessage biar
		// formatting rich tampil rapi di saluran.
		_, err = waClient.SendMessage(ctx, target.JID, &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(p.caption),
			},
		})
	} else {
		err = sendMediaToNewsletter(ctx, target.JID, p.media)
	}

	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim ke saluran: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ Berhasil dikirim ke saluran *%s*!", target.Name))
}

// sendMediaToNewsletter: newsletter pakai UploadNewsletter (unencrypted)
// + SendRequestExtra{MediaHandle} — berbeda dari upload grup biasa.
// sendMediaToNewsletter upload & kirim ke newsletter (channel).
// Newsletter pakai UploadNewsletter (TIDAK dienkripsi) — MediaKey & FileEncSHA256
// TIDAK diisi karena tidak relevan untuk unencrypted newsletter media.
// Ref: whatsmeow/upload.go UploadNewsletter docs.
func sendMediaToNewsletter(ctx context.Context, jid types.JID, med *detectedMedia) error {
	switch med.mediaType {

	case "image":
		up, err := waClient.UploadNewsletter(ctx, med.data, whatsmeow.MediaImage)
		if err != nil {
			return fmt.Errorf("upload gambar: %w", err)
		}
		_, err = waClient.SendMessage(ctx, jid, &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: &up.FileLength,
				Mimetype:   proto.String(coalesce(med.mime, "image/jpeg")),
				Caption:    proto.String(med.caption),
			},
		}, whatsmeow.SendRequestExtra{MediaHandle: up.Handle})
		return err

	case "video", "ptv":
		up, err := waClient.UploadNewsletter(ctx, med.data, whatsmeow.MediaVideo)
		if err != nil {
			return fmt.Errorf("upload video: %w", err)
		}
		_, err = waClient.SendMessage(ctx, jid, &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: &up.FileLength,
				Mimetype:   proto.String(coalesce(med.mime, "video/mp4")),
				Caption:    proto.String(med.caption),
			},
		}, whatsmeow.SendRequestExtra{MediaHandle: up.Handle})
		return err

	case "audio":
		audioData := med.data
		if !isOpusMime(med.mime) {
			converted, cerr := convertToOpus(med.data, med.mime)
			if cerr != nil {
				return fmt.Errorf("konversi opus: %w", cerr)
			}
			audioData = converted
		}
		up, err := waClient.UploadNewsletter(ctx, audioData, whatsmeow.MediaAudio)
		if err != nil {
			return fmt.Errorf("upload audio: %w", err)
		}
		_, err = waClient.SendMessage(ctx, jid, &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: &up.FileLength,
				Mimetype:   proto.String("audio/ogg; codecs=opus"),
			},
		}, whatsmeow.SendRequestExtra{MediaHandle: up.Handle})
		return err

	case "sticker":
		up, err := waClient.UploadNewsletter(ctx, med.data, whatsmeow.MediaImage)
		if err != nil {
			return fmt.Errorf("upload sticker: %w", err)
		}
		_, err = waClient.SendMessage(ctx, jid, &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: &up.FileLength,
				Mimetype:   proto.String(coalesce(med.mime, "image/webp")),
			},
		}, whatsmeow.SendRequestExtra{MediaHandle: up.Handle})
		return err
	}
	return fmt.Errorf("tipe media tidak dikenal: %s", med.mediaType)
}

// ─── Tampilan daftar saluran ──────────────────────────────────────────────────

// sendChannelList kirim daftar saluran sebagai interactive single_select
// (dropdown) — klik row = kirim command "<cmd> <index>" (diextract jadi teks,
// diproses handler yang sama). cmd dipakai sebagai prefix row id, contoh:
// "kirimch" → row id "!kirimch 2"; "playch" → "!playch 2".
// Fallback berantai: interactive → ListMessage → teks biasa.
func sendChannelList(ctx context.Context, chat types.JID, channels []channelEntry, header, cmd string) {
	const maxRowsPerSection = 10 // batas aman row per section di WA

	var sections []listSection
	for start := 0; start < len(channels); start += maxRowsPerSection {
		end := start + maxRowsPerSection
		if end > len(channels) {
			end = len(channels)
		}
		var rows []listRow
		for i := start; i < end; i++ {
			name := channels[i].Name
			if len(name) > 24 { // batas judul row di WA
				name = name[:23] + "…"
			}
			rows = append(rows, listRow{
				id:    fmt.Sprintf("%s%s %d", Prefix, cmd, i),
				title: name,
				desc:  roleEmoji(channels[i].Role) + fmt.Sprintf(" %d subs", channels[i].Subscribers),
			})
		}
		sections = append(sections, listSection{
			title: fmt.Sprintf("Saluran %d–%d", start+1, end),
			rows:  rows,
		})
	}

	b := NewMsgBuilder().
		SetHeader("📡 PILIH SALURAN", fmt.Sprintf("%d saluran — klik untuk pilih", len(channels))).
		SetBody(header).
		SetFooter("Kadaluarsa 3 menit").
		AddSelectButton("Pilih Saluran", sections)
	if sendErr := b.Send(ctx, chat); sendErr == nil {
		return
	} else {
		pool.logger.Warn().Err(sendErr).Msg("sendChannelList: interactive gagal, coba ListMessage")
	}

	// Fallback 1: ListMessage (protokol lama, banyak didukung).
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
			Title:       proto.String("📡 PILIH SALURAN"),
			Description: proto.String(header),
			ButtonText:  proto.String("Pilih Saluran"),
			FooterText:  proto.String("Kadaluarsa 3 menit"),
			ListType:    waE2E.ListMessage_SINGLE_SELECT.Enum(),
			Sections:    lmSections,
		},
	}
	if _, sendErr := waClient.SendMessage(ctx, chat, listMsg); sendErr == nil {
		return
	} else {
		pool.logger.Warn().Err(sendErr).Msg("sendChannelList: ListMessage gagal, fallback teks")
	}

	// Fallback 2: teks biasa (format lama, paling kompatibel).
	var sb strings.Builder
	fmt.Fprintf(&sb, "╔═ 『 📡 PILIH SALURAN 』\n")
	fmt.Fprintf(&sb, "║ %s\n", header)
	fmt.Fprintf(&sb, "╠══════════════════════════\n")
	for i, c := range channels {
		fmt.Fprintf(&sb, "║ *[%d]* %s %s\n", i, roleEmoji(c.Role), c.Name)
	}
	fmt.Fprintf(&sb, "╠══════════════════════════\n")
	fmt.Fprintf(&sb, "║ Ketik: *%s%s 0*  atau  *%s%s nama*\n", Prefix, cmd, Prefix, cmd)
	fmt.Fprintf(&sb, "║ _(Kadaluarsa 3 menit)_\n")
	fmt.Fprintf(&sb, "╚══════════════════════════")
	sendText(ctx, chat, sb.String())
}

// ─── m!upswch — Update STATUS saluran ─────────────────────────────────────────
// Set "status channel" (teks yang tampil di header channel, BUKAN pesan chat).
// Equivalent Baileys: conn.sendMessage(jid, { newsletterAdminProfileStatusMessage })
// = waE2E.Message{ NewsletterAdminProfileStatusMessage: FutureProofMessage{
//     Message: ExtendedTextMessage{Text: teks} } } — pola sama seperti
// GroupStatusMessage di swgc2 (bukan media, jadi tidak butuh UploadNewsletter).
// ExtendedTextMessage (bukan Conversation) supaya formatting *bold* dll tampil.

func handleUpSwCh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	status := strings.TrimSpace(args)

	if SwGC2JID == "" {
		sendText(ctx, chat,
			"❌ *SwGC2JID* belum diset di `config.go`.\n"+
				"Atau pakai: *"+Prefix+"kirimch* untuk pilih saluran.")
		return
	}
	if status == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"📝 *UPDATE STATUS SALURAN*\n\n"+
				"Set teks status channel (header channel), bukan pesan chat.\n"+
				"Formatting markdown didukung: `*tebal*`, `_miring_`, `` `kode` ``.\n\n"+
				"Contoh:\n`%supswch Bot aktif *24 jam*`\n\n"+
				"🧪 Mode eksperimen (metode resmi belum terdokumentasi publik):\n"+
				"`%supswch --v1..--v6 <teks>` — tes 6 varian protokol pesan\n"+
				"`%supswch --all <teks>` — tes semua varian berurutan\n"+
				"`%supswch --mut <teks>` — tes mutation graphql (field tak terdokumentasi)",
			Prefix, Prefix, Prefix, Prefix))
		return
	}

	targetJID, err := types.ParseJID(SwGC2JID)
	if err != nil {
		sendText(ctx, chat, "❌ SwGC2JID tidak valid: "+err.Error())
		return
	}

	// Status hanya bisa di-set admin/owner channel — cek role dulu biar
	// errornya jelas daripada gagal diam-diam di sisi server.
	allChannels, err := fetchChannelList(ctx)
	if err == nil {
		found := false
		for _, c := range allChannels {
			if c.JID == targetJID {
				found = true
				if c.Role != "owner" && c.Role != "admin" {
					reactMsg(ctx, evt, "❌")
					sendText(ctx, chat, fmt.Sprintf(
						"❌ Bot bukan admin/owner di *%s*.\n"+
							"Status saluran hanya bisa di-set admin channel.",
						c.Name))
					return
				}
				break
			}
		}
		if !found {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat,
				"❌ Bot tidak mengikuti saluran target (tidak ketemu di daftar).\n"+
					"Cek dengan *"+Prefix+"listsaluran*.")
			return
		}
	}

	// ─── MODE EKSPERIMEN ────────────────────────────────────────────────────────
	// Metode resmi "set status channel" belum terdokumentasi di publik; satu-satunya
	// jalur protokol yang diketahui adalah field 126 (newsletterAdminProfileStatusMessage).
	// Karena itu kita uji SEMUA varian yang masuk akal — field 116/117/126 × inner
	// Conversation/ExtendedTextMessage — dan lihat mana yang diterima WhatsApp.
	// Varian yang tampil sebagai status channel = metode yang benar.
	// Usage: m!upswch <teks> | m!upswch --vN <teks> | m!upswch --all <teks>
	innerConv := func(t string) *waE2E.Message {
		return &waE2E.Message{Conversation: proto.String(t)}
	}
	innerExt := func(t string) *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(t)}}
	}
	wrap := func(inner *waE2E.Message) *waE2E.FutureProofMessage {
		return &waE2E.FutureProofMessage{Message: inner}
	}
	variants := []struct {
		name  string
		build func(t string) *waE2E.Message
	}{
		{"126-ext", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileStatusMessage: wrap(innerExt(t))} }},
		{"126-conv", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileStatusMessage: wrap(innerConv(t))} }},
		{"117-conv", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileMessageV2: wrap(innerConv(t))} }},
		{"117-ext", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileMessageV2: wrap(innerExt(t))} }},
		{"116-conv", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileMessage: wrap(innerConv(t))} }},
		{"116-ext", func(t string) *waE2E.Message { return &waE2E.Message{NewsletterAdminProfileMessage: wrap(innerExt(t))} }},
	}

	// Pilih varian: --vN → satu varian; --all → semua berurutan; --mut → mutation
	// graphql; default V1 (126-ext).
	sel := 0
	all := false
	mut := false
	body := status
	if len(status) > 5 && strings.HasPrefix(status, "--") {
		switch {
		case strings.HasPrefix(status, "--v"):
			if n, e := strconv.Atoi(status[3:4]); e == nil && n >= 1 && n <= len(variants) {
				sel = n - 1
				body = strings.TrimSpace(status[5:])
				if body == "" {
					sendText(ctx, chat, "❌ Teks status kosong setelah `--v"+status[3:4]+"`.")
					return
				}
			} else {
				sendText(ctx, chat, fmt.Sprintf(
					"❌ Varian tidak dikenal. Pilihan: `--v1`..`--v%d`, `--all`, atau `--mut`.\n"+
						"Contoh: `%supswch --v3 Bot aktif *24 jam*`", len(variants), Prefix))
				return
			}
		case status == "--all" || strings.HasPrefix(status, "--all "):
			all = true
			body = strings.TrimSpace(strings.TrimPrefix(status, "--all"))
		case status == "--mut" || strings.HasPrefix(status, "--mut "):
			mut = true
			body = strings.TrimSpace(strings.TrimPrefix(status, "--mut"))
			if body == "" {
				sendText(ctx, chat, "❌ Teks status kosong setelah `--mut`.")
				return
			}
		default:
			sendText(ctx, chat, fmt.Sprintf(
				"❌ Opsi tidak dikenal. Pilihan: `--v1`..`--v%d`, `--all`, atau `--mut`.",
				len(variants)))
			return
		}
	}

	reactMsg(ctx, evt, "⏳")

	// ─── MODE MUTATION ───────────────────────────────────────────────────────────
	// Status channel ternyata BUKAN pesan (6 varian protokol gagal). Hipotesis
	// berikutnya: mutation graphql xwa2_newsletter_update (id 7150902998257522)
	// dengan field `status` yang tidak terdokumentasi publik. Urutan tes:
	//   1. Kontrol positif: update description (field yang pasti valid) → bukti
	//      jalur mutation jalan.
	//   2. Coba updates.status → kalau sukses = JACKPOT, status channel = ini.
	//   3. Restore description lama supaya channel tidak berubah permanen.
	if mut {
		info, infoErr := waClient.GetNewsletterInfo(ctx, targetJID)
		if infoErr != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal ambil metadata channel: "+infoErr.Error())
			return
		}
		oldDesc := info.ThreadMeta.Description.Text
		mutIDMobile := "7150902998257522" // mutationUpdateNewsletter (mobile)
		mutIDDesktop := "7839742399440946" // mutationUpdateNewsletter (desktop/WEB)

		var sb strings.Builder
		sb.WriteString("🧪 *TES MUTATION STATUS SALURAN*\n\n")

		// 1) Kontrol positif — ubah description (bukti jalur mutation jalan).
		descTest := "KONTROL-POSITIF-" + strconv.FormatInt(time.Now().Unix(), 10)
		_, err1 := waClient.ExecuteNewsletterMutation(ctx, mutIDMobile, map[string]any{
			"newsletter_id": targetJID.String(),
			"updates":       map[string]any{"description": descTest},
		})
		if err1 != nil {
			reactMsg(ctx, evt, "❌")
			sb.WriteString("❌ Kontrol positif GAGAL (jalur mutation bermasalah):\n`" + truncateErr(err1, 150) + "`\n")
			sb.WriteString("\nKalau ini gagal, mutation bukan jalurnya — status channel tidak lewat sini.")
			sendText(ctx, chat, sb.String())
			return
		}
		sb.WriteString("✅ Kontrol positif: jalur mutation JALAN (description di-update)\n")

		// 2) Coba kandidat field status — mobile & desktop mutation ID, plus
		//    beberapa nama field yang mungkin dipakai app resmi.
		candidates := []struct {
			label   string
			field   string
			desktop bool
		}{
			{"status (mobile)", "status", false},
			{"status (desktop)", "status", true},
			{"status_text", "status_text", false},
			{"statusText", "statusText", false},
			{"announcement", "announcement", false},
			{"bio", "bio", false},
			{"header", "header", false},
			{"status_message", "status_message", false},
			{"statusMessage", "statusMessage", false},
		}
		jackpot := ""
		for _, c := range candidates {
			mutID := mutIDMobile
			if c.desktop {
				mutID = mutIDDesktop
			}
			_, err := waClient.ExecuteNewsletterMutation(ctx, mutID, map[string]any{
				"newsletter_id": targetJID.String(),
				"updates":       map[string]any{c.field: body},
			})
			if err != nil {
				sb.WriteString("❌ `" + c.label + "` ditolak: `" + truncateErr(err, 60) + "`\n")
			} else {
				jackpot = c.label
				sb.WriteString("✅ `" + c.label + "` DITERIMA server! 🎉\n")
				break // stop — jangan restore description kalau jackpot (status sudah berubah)
			}
			time.Sleep(300 * time.Millisecond)
		}
		if jackpot == "" {
			sb.WriteString("\nSemua kandidat ditolak → status BUKAN field mutation update.\n")
		} else {
			sb.WriteString("\nCek header saluran — status harusnya sudah berubah!\n")
		}

		// 3) Restore description lama (kecuali jackpot — biarkan status terpasang).
		if jackpot == "" {
			if _, err3 := waClient.ExecuteNewsletterMutation(ctx, mutIDMobile, map[string]any{
				"newsletter_id": targetJID.String(),
				"updates":       map[string]any{"description": oldDesc},
			}); err3 != nil {
				sb.WriteString("⚠️ Gagal restore description lama: `" + truncateErr(err3, 100) + "`\n")
			} else {
				sb.WriteString("♻️ Description di-restore ke semula\n")
			}
		}

		if jackpot == "" {
			reactMsg(ctx, evt, "❌")
		} else {
			reactMsg(ctx, evt, "✅")
		}
		sb.WriteString("\n💡 Alternatif: set status manual dari app resmi sambil bot jalan —\nbot akan kirim dump protobuf aslinya ke chat ini (📡 CHANNEL INCOMING).")
		sendText(ctx, chat, sb.String())
		return
	}

	runVariant := func(v struct {
		name  string
		build func(t string) *waE2E.Message
	}, text string) error {
		_, err := waClient.SendMessage(ctx, targetJID, v.build(text))
		pool.logger.Info().Str("variant", v.name).Err(err).Msg("upswch: coba varian status saluran")
		return err
	}

	if all {
		// Kirim berurutan; status yang tampil di channel = varian terakhir yang
		// berhasil. Jeda 1s antar kiriman biar urutan pesannya jelas.
		var ok []string
		var fail []string
		for _, v := range variants {
			if err := runVariant(v, body); err != nil {
				fail = append(fail, v.name+": "+truncateErr(err, 70))
			} else {
				ok = append(ok, v.name)
			}
			time.Sleep(time.Second)
		}
		var sb strings.Builder
		sb.WriteString("🧪 *EKSPERIMEN STATUS SALURAN*\n\n")
		if len(ok) > 0 {
			sb.WriteString("✅ Diterima server: `" + strings.Join(ok, "`, `") + "`\n")
		} else {
			sb.WriteString("❌ Semua varian gagal di sisi server.\n")
		}
		if len(fail) > 0 {
			sb.WriteString("\n❌ Gagal:\n" + strings.Join(fail, "\n") + "\n")
		}
		sb.WriteString(fmt.Sprintf(
			"\n📖 Status channel sekarang = varian TERAKHIR yang diterima.\n"+
				"Cek header saluran. Kalau kosong/tidak berubah, tidak ada varian yang benar,\n"+
				"dan metode status channel bukan lewat field ini.\n\n"+
				"Tes satu-satu: `%supswch --v1..--v%d <teks>`", Prefix, len(variants)))
		if len(ok) > 0 {
			reactMsg(ctx, evt, "✅")
		} else {
			reactMsg(ctx, evt, "❌")
		}
		sendText(ctx, chat, sb.String())
		return
	}

	if err := runVariant(variants[sel], body); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Gagal set status saluran (varian `%s`): %s",
			variants[sel].name, err.Error()))
		return
	}
	reactMsg(ctx, evt, "✅")

	// Konfirmasi rich + tombol: buka saluran (CTA URL). Fallback ke teks biasa
	// kalau interactive gagal. (Tombol "Ganti Status" dihapus — fitur disabled.)
	channelURL := "https://whatsapp.com/channel/" + targetJID.User
	ctrl := NewMsgBuilder().
		SetHeader("📝 STATUS SALURAN", "berhasil di-update").
		SetBody(fmt.Sprintf("✅ Status *%s* di-update:\n\n📝 %s", targetJID.User, status)).
		SetFooter("Perubahan tampil di header saluran").
		AddCTAURL("🌐 Buka Saluran", channelURL)
	if sendErr := ctrl.Send(ctx, chat); sendErr != nil {
		pool.logger.Warn().Err(sendErr).Msg("upswch: konfirmasi interactive gagal, fallback teks")
		sendText(ctx, chat, fmt.Sprintf("✅ Status saluran di-update:\n\n📝 *%s*", status))
	}
}

// handleUpCh — alias untuk m!upch/m!sendch, kirim ke SwGC2JID (config).
func handleUpCh(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat

	if SwGC2JID == "" {
		sendText(ctx, chat,
			"❌ *SwGC2JID* belum diset di `config.go`.\n"+
				"Atau pakai: *"+Prefix+"kirimch* untuk pilih saluran.")
		return
	}

	targetJID, err := types.ParseJID(SwGC2JID)
	if err != nil {
		sendText(ctx, chat, "❌ SwGC2JID tidak valid: "+err.Error())
		return
	}

	args := getArgs(evt, "upch")
	if args == "" {
		args = getArgs(evt, "sendch")
	}

	reactMsg(ctx, evt, "⏳")

	med, err := detectMedia(ctx, evt)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}

	if med == nil {
		if args == "" {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, fmt.Sprintf(
				"❌ Reply media atau tulis teks.\n"+
					"*Atau pakai:* *%skirimch* untuk pilih saluran.", Prefix))
			return
		}
		// ExtendedTextMessage biar formatting rich (bold/code) ikut tampil
		// rapi di klien — saluran mendukung formatting seperti chat biasa.
		_, err = waClient.SendMessage(ctx, targetJID, &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(args),
			},
		})
	} else {
		if args != "" {
			med.caption = args
		}
		sendText(ctx, chat, fmt.Sprintf("⏳ Mengupload %s ke saluran...", med.mediaType))
		err = sendMediaToNewsletter(ctx, targetJID, med)
	}

	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "✅ Berhasil dikirim ke saluran!")
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// getArgs ambil teks setelah prefix+command dari pesan.
// Fix bug: fallback lama return seluruh teks (termasuk "!gstatus") yang
// lalu jadi caption ikut terkirim ke saluran/grup.
// Sekarang: kalau alias tidak cocok, strip "!<command>" apapun dari depan.
func getArgs(evt *events.Message, cmd string) string {
	raw := evt.Message.GetConversation()
	if raw == "" {
		raw = evt.Message.GetExtendedTextMessage().GetText()
	}
	raw = strings.TrimSpace(raw)

	// Coba exact match dengan cmd yang diberikan
	full := Prefix + cmd
	if strings.HasPrefix(strings.ToLower(raw), strings.ToLower(full)) {
		return strings.TrimSpace(raw[len(full):])
	}

	// Fallback: kalau pesan dimulai dengan prefix "!", strip "!<kata pertama>"
	// ini handle semua alias (gstatus, kirim, upch, dll) tanpa harus tau nama pastinya
	if strings.HasPrefix(raw, Prefix) {
		rest := strings.TrimSpace(raw[len(Prefix):])
		fields := strings.Fields(rest)
		if len(fields) <= 1 {
			return "" // cuma command, tidak ada args
		}
		// return semua setelah command pertama
		return strings.TrimSpace(rest[len(fields[0]):])
	}

	return "" // bukan command — return kosong, jangan kembalikan raw
}

// ─── m!fetchch — Fetch pesan channel (debug protobuf) ───────────────────────
// Ambil N pesan terakhir dari SwGC2JID dan dump protobuf-nya ke chat pemanggil.
// Dipakai buat membedah bentuk asli pesan status channel yang dikirim app resmi:
//   1. !fetchch 5            → lihat pesan terakhir channel (sebelum set status)
//   2. set status manual dari app WhatsApp resmi
//   3. !fetchch 5 lagi       → kalau status = pesan, bentuknya muncul di dump
func handleFetchCh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat

	count := 10
	if n, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && n > 0 && n <= 50 {
		count = n
	}

	if SwGC2JID == "" {
		sendText(ctx, chat, "❌ *SwGC2JID* belum diset di `config.go`.")
		return
	}
	targetJID, err := types.ParseJID(SwGC2JID)
	if err != nil {
		sendText(ctx, chat, "❌ SwGC2JID tidak valid: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "⏳")
	msgs, err := waClient.GetNewsletterMessages(ctx, targetJID, &whatsmeow.GetNewsletterMessagesParams{Count: count})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch pesan channel: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")

	if len(msgs) == 0 {
		sendText(ctx, chat, "📭 Tidak ada pesan di channel.")
		return
	}

	// Pesan-pesan ini mewakili konten saluran → tampil seperti "diteruskan
	// dari saluran" (newsletterCtxInfo), konsisten dengan menu.
	ctxInfo := newsletterCtxInfo(ctx)
	sendTextWithCtx(ctx, chat, fmt.Sprintf(
		"📡 *FETCH CHANNEL* — %d pesan terakhir dari `%s`\n"+
			"Cari pesan berisi `newsletterAdminProfile*` — itu bentuk status channel.",
		len(msgs), targetJID), ctxInfo)
	for i, m := range msgs {
		raw := ""
		if m.Message != nil {
			if b, mErr := prototext.Marshal(m.Message); mErr == nil {
				raw = string(b)
			} else {
				raw = "<marshal error: " + mErr.Error() + ">"
			}
		} else {
			raw = "<no message payload>"
		}
		if len(raw) > 1500 {
			raw = raw[:1500] + "…"
		}
		sendTextWithCtx(ctx, chat, fmt.Sprintf(
			"📦 *%d* — server_id: `%d` | type: `%s` | ts: `%s`\n```%s```",
			i+1, m.MessageServerID, m.Type, m.Timestamp.Format("15:04:05"), raw), ctxInfo)
	}
}

// truncateErr memotong pesan error ke maks `n` karakter (dengan elipsis) supaya
// ringkasan eksperimen upswch tidak kepanjangan di chat.
func truncateErr(err error, n int) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
