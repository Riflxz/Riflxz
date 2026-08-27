package main

// chat_formats.go — Fitur "bentuk chat" modern (port dari fitur Baileys modern).
// Baileys (Node.js) punya banyak helper send*: sendPoll, sendContact, sendLocation,
// sendViewOnce, sendPTV, editMessage, dll. Bot kita pakai whatsmeow (Go) — semua
// bentuk ini didukung protokol WhatsApp via waE2E.Message langsung.
//
// Fitur:
//   - !poll <pertanyaan> | <opsi1> | <opsi2> ...   → PollCreationMessage
//   - !kontak <nama> | <nomor>                     → ContactMessage (vCard)
//   - !lokasi <lat>,<lng> [label]                  → LocationMessage
//   - !vo (reply gambar/video)                     → ViewOnceMessage
//   - !ptv (reply video)                           → PtvMessage (video singkat)
//   - !edit <teks baru> (reply pesan)              → EditMessage (BuildEdit)

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Poll ─────────────────────────────────────────────────────────────────────
// !poll <pertanyaan> | <opsi1> | <opsi2> ... (max 12 opsi, min 2)

func handlePoll(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.Split(args, "|")
	if len(parts) < 3 {
		sendText(ctx, chat, fmt.Sprintf(
			"📊 *Poll*\n\n"+
				"> Buat polling interaktif\n\n"+
				"*Format:*\n"+
				"> `%spoll <pertanyaan> | <opsi1> | <opsi2> ...`\n\n"+
				"*Contoh:*\n"+
				"> `%spoll Makan apa? | Nasi goreng | Mie ayam | Sate`",
			Prefix, Prefix))
		return
	}
	question := strings.TrimSpace(parts[0])
	var options []string
	for _, p := range parts[1:] {
		opt := strings.TrimSpace(p)
		if opt != "" {
			options = append(options, opt)
		}
	}
	if question == "" || len(options) < 2 {
		sendText(ctx, chat, "❌ Pertanyaan & minimal 2 opsi wajib diisi.")
		return
	}
	if len(options) > 12 {
		options = options[:12]
	}

	msg := waClient.BuildPollCreation(question, options, 1)
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim poll: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📊")
}

// ─── Kontak (vCard) ───────────────────────────────────────────────────────────
// !kontak <nama> | <nomor>

func handleContact(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.SplitN(args, "|", 2)
	if len(parts) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"👤 *Kontak*\n\n"+
				"> Kirim kartu kontak (vCard)\n\n"+
				"*Format:*\n"+
				"> `%skontak <nama> | <nomor>`\n\n"+
				"*Contoh:*\n"+
				"> `%skontak Budi | 6281234567890`",
			Prefix, Prefix))
		return
	}
	name := strings.TrimSpace(parts[0])
	number := strings.TrimSpace(parts[1])
	number = strings.TrimPrefix(number, "+")
	if name == "" || number == "" {
		sendText(ctx, chat, "❌ Nama & nomor wajib diisi.")
		return
	}

	vcard := fmt.Sprintf(
		"BEGIN:VCARD\nVERSION:3.0\nFN:%s\nN:%s;;;\nTEL;type=CELL;waid=%s:+%s\nEND:VCARD",
		name, name, number, number)

	msg := &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(name),
			Vcard:       proto.String(vcard),
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim kontak: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "👤")
}

// ─── Lokasi ───────────────────────────────────────────────────────────────────
// !lokasi <lat>,<lng> [label]

func handleLocation(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) < 1 {
		sendText(ctx, chat, fmt.Sprintf(
			"📍 *Lokasi*\n\n"+
				"> Kirim titik lokasi\n\n"+
				"*Format:*\n"+
				"> `%slokasi <lat>,<lng> [label]`\n\n"+
				"*Contoh:*\n"+
				"> `%slokasi -6.200000,106.816666 Monas`",
			Prefix, Prefix))
		return
	}
	coords := strings.Split(strings.TrimSpace(fields[0]), ",")
	if len(coords) < 2 {
		sendText(ctx, chat, "❌ Format koordinat salah. Contoh: `-6.2,106.8`")
		return
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(coords[0]), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(coords[1]), 64)
	if err1 != nil || err2 != nil {
		sendText(ctx, chat, "❌ Koordinat harus angka. Contoh: `-6.2,106.8`")
		return
	}
	label := strings.Join(fields[1:], " ")

	msg := &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(lat),
			DegreesLongitude: proto.Float64(lng),
			Name:             proto.String(label),
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim lokasi: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📍")
}

// ─── View Once ────────────────────────────────────────────────────────────────
// !vo (reply gambar/video) — kirim ulang sebagai view-once

func handleViewOnce(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, "❌ Reply ke gambar/video dulu, lalu ketik `"+Prefix+"vo`")
		return
	}

	var media *waE2E.Message
	if img := quoted.GetImageMessage(); img != nil {
		media = &waE2E.Message{ImageMessage: img}
	} else if vid := quoted.GetVideoMessage(); vid != nil {
		media = &waE2E.Message{VideoMessage: vid}
	} else {
		sendText(ctx, chat, "❌ Hanya gambar atau video yang bisa dijadikan view-once.")
		return
	}

	reactMsg(ctx, evt, "⏳")
	msg := &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{
			Message: media,
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim view-once: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "👁️")
}

// ─── PTV (Picture-to-Video) ───────────────────────────────────────────────────
// !ptv (reply video) — kirim video sebagai PTV (video singkat seperti GIF)

func handlePTV(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetVideoMessage() == nil {
		sendText(ctx, chat, "❌ Reply ke video dulu, lalu ketik `"+Prefix+"ptv`")
		return
	}

	reactMsg(ctx, evt, "⏳")
	msg := &waE2E.Message{
		PtvMessage: quoted.GetVideoMessage(),
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim PTV: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "🎬")
}

// ─── Edit Pesan ───────────────────────────────────────────────────────────────
// !edit <teks baru> (reply pesan bot sendiri) — edit dalam window 20 menit

func handleEdit(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	newText := strings.TrimSpace(args)
	if newText == "" {
		sendText(ctx, chat, "❌ Contoh: reply pesan bot + ketik `"+Prefix+"edit teks baru`")
		return
	}
	ci := msgContextInfo(evt)
	stanzaID := ci.GetStanzaID()
	if stanzaID == "" {
		sendText(ctx, chat, "❌ Reply ke pesan yang mau diedit dulu.")
		return
	}

	// Edit juga sebagai ExtendedTextMessage — kalau pakai Conversation polos,
	// hasil edit kehilangan formatting rich (bold/italic) dari pesan aslinya.
	msg := waClient.BuildEdit(chat, types.MessageID(stanzaID), &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(newText),
		},
	})
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal edit pesan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✏️")
}

// ─── Live Location ────────────────────────────────────────────────────────────
// !livlok <lat>,<lng> [durasi menit] — share lokasi real-time (default 5 menit)

func handleLiveLocation(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) < 1 {
		sendText(ctx, chat, fmt.Sprintf(
			"📍 *Live Location*\n\n"+
				"> Share lokasi real-time\n\n"+
				"*Format:*\n"+
				"> `%slivlok <lat>,<lng> [durasi menit]`\n\n"+
				"*Contoh:*\n"+
				"> `%slivlok -6.200000,106.816666 10`",
			Prefix, Prefix))
		return
	}
	coords := strings.Split(strings.TrimSpace(fields[0]), ",")
	if len(coords) < 2 {
		sendText(ctx, chat, "❌ Format koordinat salah. Contoh: `-6.2,106.8`")
		return
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(coords[0]), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(coords[1]), 64)
	if err1 != nil || err2 != nil {
		sendText(ctx, chat, "❌ Koordinat harus angka. Contoh: `-6.2,106.8`")
		return
	}
	durationMin := 5
	if len(fields) > 1 {
		if d, err := strconv.Atoi(fields[1]); err == nil && d > 0 {
			durationMin = d
		}
	}
	if durationMin > 60 {
		durationMin = 60
	}
	// Sequence number: penting untuk live location (0 = mulai, naik per update).
	seq := time.Now().Unix()

	msg := &waE2E.Message{
		LiveLocationMessage: &waE2E.LiveLocationMessage{
			DegreesLatitude:  proto.Float64(lat),
			DegreesLongitude: proto.Float64(lng),
			AccuracyInMeters: proto.Uint32(10),
			Caption:          proto.String(fmt.Sprintf("📍 Live Location — %d menit", durationMin)),
			SequenceNumber:   proto.Int64(seq),
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim live location: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📍")
}

// ─── Template Buttons ─────────────────────────────────────────────────────────
// !template <teks> | <label1>|<id1> | <label2>|<id2> ...
// !template <teks> | url:<label>|<url> — tombol URL

func handleTemplateMsg(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.Split(args, "|")
	if len(parts) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"🔘 *Template Buttons*\n\n"+
				"> Pesan dengan tombol (quick reply / URL)\n\n"+
				"*Format:*\n"+
				"> `%stemplate <teks> | <label1>|<id1> | <label2>|<id2>`\n"+
				"> `%stemplate <teks> | url:<label>|<url>`\n\n"+
				"*Contoh:*\n"+
				"> `%stemplate Pilih menu | Menu A|a | Menu B|b`\n"+
				"> `%stemplate Kunjungi | url:Website|https://example.com`",
			Prefix, Prefix, Prefix, Prefix))
		return
	}
	text := strings.TrimSpace(parts[0])
	if text == "" {
		sendText(ctx, chat, "❌ Teks pesan wajib diisi.")
		return
	}

	mb := NewButton().SetBody(text).SetFooter(BotName + " — " + Prefix + "template")
	for _, p := range parts[1:] {
		label, val := "", ""
		if idx := strings.Index(p, "|"); idx >= 0 {
			label, val = strings.TrimSpace(p[:idx]), strings.TrimSpace(p[idx+1:])
		} else {
			label = strings.TrimSpace(p)
		}
		if label == "" {
			continue
		}
		if strings.HasPrefix(label, "url:") {
			btnLabel := strings.TrimSpace(strings.TrimPrefix(label, "url:"))
			if btnLabel == "" || val == "" {
				continue
			}
			mb.AddURL(btnLabel, val, false)
			continue
		}
		mb.AddReply(label, val)
	}
	if len(mb.buttons) == 0 {
		sendText(ctx, chat, "❌ Minimal 1 tombol wajib diisi.")
		return
	}

	if _, err := mb.Send(ctx, chat); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim template: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "🔘")
}

var _ = waCommon.MessageKey{}

// ─── Tabel ────────────────────────────────────────────────────────────────────
// !tabel <judul> | <kolom1>,<kolom2>,... | <baris1a>,<baris1b> | <baris2a>,<baris2b>
// Render tabel asli WhatsApp via AIRich (GenATableUXPrimitive).

func handleTable(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	parts := strings.Split(args, "|")
	if len(parts) < 3 {
		sendText(ctx, chat, fmt.Sprintf(
			"📊 *Tabel*\n\n"+
				"> Render tabel asli WhatsApp (AI rich response)\n\n"+
				"*Format:*\n"+
				"> `%stabel <judul> | <kolom1>,<kolom2> | <baris1a>,<baris1b> | ...`\n\n"+
				"*Contoh:*\n"+
				"> `%stabel Daftar Harga | Item | Harga | Nasi goreng | 15000 | Mie ayam | 12000`",
			Prefix, Prefix))
		return
	}
	title := strings.TrimSpace(parts[0])
	headerParts := strings.Split(parts[1], ",")
	var headers []string
	for _, h := range headerParts {
		if h = strings.TrimSpace(h); h != "" {
			headers = append(headers, h)
		}
	}
	if len(headers) < 1 {
		sendText(ctx, chat, "❌ Minimal 1 kolom wajib diisi.")
		return
	}
	var rows [][]string
	for _, p := range parts[2:] {
		cells := strings.Split(p, ",")
		var row []string
		for _, c := range cells {
			if c = strings.TrimSpace(c); c != "" {
				row = append(row, c)
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		sendText(ctx, chat, "❌ Minimal 1 baris data wajib diisi.")
		return
	}

	table := append([][]string{headers}, rows...)
	if _, err := NewAIRich().
		SetTitle("📊 "+title).
		AddTable(table).
		SetFooter("Dibuat dengan "+Prefix+"tabel").
		Send(ctx, chat); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim tabel: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📊")
}

// ─── Dokumen ──────────────────────────────────────────────────────────────────
// !doc <nama file> (reply media) — kirim ulang media sebagai dokumen.

func handleDoc(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fileName := strings.TrimSpace(args)
	if fileName == "" {
		sendText(ctx, chat, "❌ Contoh: reply media + ketik `"+Prefix+"doc laporan.pdf`")
		return
	}
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, "❌ Reply ke media dulu, lalu ketik `"+Prefix+"doc <nama file>`")
		return
	}

	var media *waE2E.Message
	switch {
	case quoted.GetImageMessage() != nil:
		media = &waE2E.Message{ImageMessage: quoted.GetImageMessage()}
	case quoted.GetVideoMessage() != nil:
		media = &waE2E.Message{VideoMessage: quoted.GetVideoMessage()}
	case quoted.GetAudioMessage() != nil:
		media = &waE2E.Message{AudioMessage: quoted.GetAudioMessage()}
	case quoted.GetDocumentMessage() != nil:
		media = &waE2E.Message{DocumentMessage: quoted.GetDocumentMessage()}
	default:
		sendText(ctx, chat, "❌ Reply ke gambar/video/audio/dokumen dulu.")
		return
	}

	reactMsg(ctx, evt, "⏳")
	msg := &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: media,
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim dokumen: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📄")
}

// ─── Pin Pesan ────────────────────────────────────────────────────────────────
// !pin (reply pesan) — sematkan pesan untuk semua anggota grup.

func handlePin(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	stanzaID := ci.GetStanzaID()
	if stanzaID == "" {
		sendText(ctx, chat, "❌ Reply ke pesan yang mau disematkan dulu.")
		return
	}

	msg := &waE2E.Message{
		PinInChatMessage: &waE2E.PinInChatMessage{
			Key: &waCommon.MessageKey{
				FromMe:    proto.Bool(true),
				ID:        proto.String(stanzaID),
				RemoteJID: proto.String(chat.String()),
			},
			Type:              waE2E.PinInChatMessage_PIN_FOR_ALL.Enum(),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}
	if _, err := waClient.SendMessage(ctx, chat, msg); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal pin pesan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "📌")
}
