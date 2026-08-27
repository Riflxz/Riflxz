package main

// group_features.go — dua fitur terpisah:
//
// 1. m!swgc2  — Send WhatsApp Group Chat Status (tab "Updates" di grup).
//               Reply media → m!swgc2 → pilih grup → kirim sebagai group status.
//               Sama persis dengan { groupStatusMessage: payload } di Baileys.
//
// 2. m!upch / m!sendch — Upload media ke channel/newsletter (SwGC2JID di config).
//               Reply media → m!upch → langsung kirim ke channel.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Media detection & conversion ────────────────────────────────────────────

type detectedMedia struct {
	mediaType string // "image"|"video"|"ptv"|"audio"|"sticker"
	data      []byte
	mime      string
	caption   string
	isPTT     bool
}

// unwrapMsg buka layer ephemeral/viewOnce/documentWithCaption/editedMessage.
// Port dari fungsi unwrapMessage() di JS.
func unwrapMsg(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}
	if e := msg.GetEphemeralMessage(); e != nil && e.Message != nil {
		return unwrapMsg(e.Message)
	}
	if v := msg.GetViewOnceMessage(); v != nil && v.Message != nil {
		return unwrapMsg(v.Message)
	}
	if v := msg.GetViewOnceMessageV2(); v != nil && v.Message != nil {
		return unwrapMsg(v.Message)
	}
	if d := msg.GetDocumentWithCaptionMessage(); d != nil && d.Message != nil {
		return unwrapMsg(d.Message)
	}
	if e := msg.GetEditedMessage(); e != nil && e.Message != nil {
		return unwrapMsg(e.Message)
	}
	return msg
}

// detectMedia cari media dari quoted message atau pesan itu sendiri.
// Port dari fungsi detectMedia() di JS.
func detectMedia(ctx context.Context, evt *events.Message) (*detectedMedia, error) {
	candidates := []*waE2E.Message{}

	// Quoted dulu (prioritas utama)
	if ci := msgContextInfo(evt); ci != nil {
		if q := ci.GetQuotedMessage(); q != nil {
			candidates = append(candidates, q)
		}
	}
	candidates = append(candidates, evt.Message)

	for _, raw := range candidates {
		m := unwrapMsg(raw)
		if m == nil {
			continue
		}

		if img := m.GetImageMessage(); img != nil {
			data, err := waClient.Download(ctx, img)
			if err != nil {
				return nil, fmt.Errorf("gagal download gambar: %w", err)
			}
			return &detectedMedia{
				mediaType: "image",
				data:      data,
				mime:      coalesce(img.GetMimetype(), "image/jpeg"),
				caption:   img.GetCaption(),
			}, nil
		}

		if vid := m.GetVideoMessage(); vid != nil {
			data, err := waClient.Download(ctx, vid)
			if err != nil {
				return nil, fmt.Errorf("gagal download video: %w", err)
			}
			return &detectedMedia{
				mediaType: "video",
				data:      data,
				mime:      coalesce(vid.GetMimetype(), "video/mp4"),
				caption:   vid.GetCaption(),
			}, nil
		}

		if ptv := m.GetPtvMessage(); ptv != nil {
			data, err := waClient.Download(ctx, ptv)
			if err != nil {
				return nil, fmt.Errorf("gagal download ptv: %w", err)
			}
			return &detectedMedia{
				mediaType: "ptv",
				data:      data,
				mime:      coalesce(ptv.GetMimetype(), "video/mp4"),
			}, nil
		}

		if aud := m.GetAudioMessage(); aud != nil {
			data, err := waClient.Download(ctx, aud)
			if err != nil {
				return nil, fmt.Errorf("gagal download audio: %w", err)
			}
			return &detectedMedia{
				mediaType: "audio",
				data:      data,
				mime:      coalesce(aud.GetMimetype(), "audio/ogg; codecs=opus"),
				isPTT:     aud.GetPTT(),
			}, nil
		}

		if stk := m.GetStickerMessage(); stk != nil {
			data, err := waClient.Download(ctx, stk)
			if err != nil {
				return nil, fmt.Errorf("gagal download sticker: %w", err)
			}
			return &detectedMedia{
				mediaType: "sticker",
				data:      data,
				mime:      coalesce(stk.GetMimetype(), "image/webp"),
			}, nil
		}
	}
	return nil, nil
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// mimeToExt: port dari MIME_EXT map di JS.
func mimeToExt(mime string) string {
	clean := strings.TrimSpace(strings.ToLower(strings.SplitN(mime, ";", 2)[0]))
	m := map[string]string{
		"image/jpeg": "jpg", "image/jpg": "jpg", "image/png": "png",
		"image/webp": "webp", "image/gif": "gif",
		"video/mp4": "mp4", "video/mpeg": "mpeg", "video/3gpp": "3gp",
		"audio/ogg": "ogg", "audio/mpeg": "mp3", "audio/mp4": "m4a",
		"audio/aac": "aac", "audio/wav": "wav",
	}
	if ext, ok := m[clean]; ok {
		return ext
	}
	if p := strings.SplitN(clean, "/", 2); len(p) == 2 {
		return p[1]
	}
	return "bin"
}

// convertToOpus: port dari convertToOpus() JS (fluent-ffmpeg → exec ffmpeg).
func convertToOpus(data []byte, inputMime string) ([]byte, error) {
	if err := os.MkdirAll("temp", 0o755); err != nil {
		return nil, err
	}
	uid := uuid.New().String()
	inPath := filepath.Join("temp", "au_in_"+uid+"."+mimeToExt(inputMime))
	outPath := filepath.Join("temp", "au_out_"+uid+".ogg")
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	if err := os.WriteFile(inPath, data, 0o644); err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath, "-c:a", "libopus", "-b:a", "64k", "-f", "ogg", outPath)
	cmd.Stderr = &stderr
	// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
	if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
		return nil, fmt.Errorf("ffmpeg: %s", lastLines(stderr.String(), 3))
	}
	return os.ReadFile(outPath)
}

func isOpusMime(mime string) bool {
	m := strings.ToLower(mime)
	return strings.Contains(m, "opus") || strings.Contains(m, "ogg")
}

// ─── Build inner message (untuk sendMediaToJID & group status) ───────────────

func buildInnerMsg(med *detectedMedia) (*waE2E.Message, error) {
	switch med.mediaType {

	case "image":
		up, err := waClient.Upload(context.Background(), med.data, whatsmeow.MediaImage)
		if err != nil {
			return nil, fmt.Errorf("upload gambar: %w", err)
		}
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(uint64(len(med.data))),
			Mimetype:   proto.String(med.mime), Caption: proto.String(med.caption),
		}}, nil

	case "video", "ptv":
		up, err := waClient.Upload(context.Background(), med.data, whatsmeow.MediaVideo)
		if err != nil {
			return nil, fmt.Errorf("upload video: %w", err)
		}
		gif := med.mediaType == "ptv"
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(uint64(len(med.data))),
			Mimetype:   proto.String(med.mime), Caption: proto.String(med.caption),
			GifPlayback: proto.Bool(gif),
		}}, nil

	case "audio":
		audioData := med.data
		if !isOpusMime(med.mime) {
			converted, err := convertToOpus(med.data, med.mime)
			if err != nil {
				return nil, fmt.Errorf("konversi opus: %w", err)
			}
			audioData = converted
		}
		up, err := waClient.Upload(context.Background(), audioData, whatsmeow.MediaAudio)
		if err != nil {
			return nil, fmt.Errorf("upload audio: %w", err)
		}
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(uint64(len(audioData))),
			Mimetype:   proto.String("audio/ogg; codecs=opus"),
			PTT:        proto.Bool(true),
		}}, nil

	case "sticker":
		up, err := waClient.Upload(context.Background(), med.data, whatsmeow.MediaImage)
		if err != nil {
			return nil, fmt.Errorf("upload sticker: %w", err)
		}
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(uint64(len(med.data))),
			Mimetype:   proto.String(med.mime),
		}}, nil
	}
	return nil, fmt.Errorf("tipe media tidak dikenal: %s", med.mediaType)
}

// ─── m!swgc2 — Send WhatsApp Group Chat STATUS ───────────────────────────────
// Kirim sebagai group status (tab "Updates" di grup), bukan pesan biasa.
// Equivalent Baileys: conn.sendMessage(jid, { groupStatusMessage: payload })

type groupEntry struct {
	JID         types.JID
	Subject     string
	MemberCount int
}

type pendingGroupSend struct {
	groups    []groupEntry
	media     *detectedMedia // nil = teks
	caption   string
	expiresAt time.Time
	// Fix: anti double-send (lihat sendGroupStatus).
	mu      sync.Mutex
	claimed bool
}

var (
	groupPendingMu sync.Mutex
	groupPending   = map[string]*pendingGroupSend{}
)

func fetchGroupList(ctx context.Context) ([]groupEntry, error) {
	groups, err := waClient.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	var list []groupEntry
	for _, g := range groups {
		list = append(list, groupEntry{JID: g.JID, Subject: g.Name, MemberCount: len(g.Participants)})
	}
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Subject) < strings.ToLower(list[j].Subject)
	})
	return list, nil
}

func filterGroups(list []groupEntry, q string) []groupEntry {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []groupEntry
	for _, g := range list {
		if strings.Contains(strings.ToLower(g.Subject), q) {
			out = append(out, g)
		}
	}
	return out
}

func isNumericOnly(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(s) > 0
}

func cleanExpiredGroupPending() {
	groupPendingMu.Lock()
	defer groupPendingMu.Unlock()
	now := time.Now()
	for k, v := range groupPending {
		if now.After(v.expiresAt) {
			delete(groupPending, k)
		}
	}
}

// handleSWGC2 — args datang dari router (bukan getArgs): router mengekstrak
// teks termasuk button id dari InteractiveResponseMessage (extractButtonCommand),
// sedangkan getArgs membaca protobuf Conversation/ExtendedText yang KOSONG untuk
// pesan hasil klik tombol — dulu itu bikin pending tidak ketemu lalu bot minta
// reply media lagi.
func handleSWGC2(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	user := senderUser(evt)
	cleanExpiredGroupPending()

	// ── Cek pending state ─────────────────────────────────────────────────────
	groupPendingMu.Lock()
	pending, hasPending := groupPending[user]
	groupPendingMu.Unlock()

	if hasPending && time.Now().After(pending.expiresAt) {
		groupPendingMu.Lock()
		delete(groupPending, user)
		groupPendingMu.Unlock()
		hasPending = false
		sendText(ctx, chat, "⌛ Sesi kadaluarsa. Ulangi *"+Prefix+"swgc2*.")
	}

	if hasPending && args != "" {
		// Pilih dengan nomor
		if isNumericOnly(args) {
			// Fix: validasi hasil Atoi — isNumericOnly bisa lolos buat angka
			// raksasa (>int64), Atoi error → idx=0 yang salah sasaran.
			idx, err := strconv.Atoi(args)
			if err != nil || idx < 0 || idx >= len(pending.groups) {
				sendText(ctx, chat, fmt.Sprintf("❌ Pilih nomor 0–%d.", len(pending.groups)-1))
				return
			}
			sendGroupStatus(ctx, evt, user, pending, pending.groups[idx])
			return
		}
		// Cari nama
		filtered := filterGroups(pending.groups, args)
		if len(filtered) == 1 {
			sendGroupStatus(ctx, evt, user, pending, filtered[0])
			return
		} else if len(filtered) > 1 {
			pending.groups = filtered
			pending.expiresAt = time.Now().Add(3 * time.Minute)
			groupPendingMu.Lock()
			groupPending[user] = pending
			groupPendingMu.Unlock()
			sendGroupList(ctx, chat, filtered, fmt.Sprintf("🔍 %d grup cocok. Pilih:", len(filtered)))
			return
		}
		sendText(ctx, chat, fmt.Sprintf("❌ Grup *%s* tidak ditemukan.", args))
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
				"① Reply gambar/video/audio/sticker → *%sswgc2*\n"+
				"② Atau: *%sswgc2 <teks pesan>*\n\n"+
				"Bot tampilkan daftar grup, lalu kirim nomor atau nama grup.",
			Prefix, Prefix))
		return
	}

	caption := args
	if med != nil && caption != "" {
		med.caption = caption
	}

	allGroups, err := fetchGroupList(ctx)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar grup: "+err.Error())
		return
	}
	if len(allGroups) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Bot tidak berada di grup manapun.")
		return
	}

	newPending := &pendingGroupSend{
		groups:    allGroups,
		media:     med,
		caption:   caption,
		expiresAt: time.Now().Add(3 * time.Minute),
	}

	// Kalau args = nama grup, langsung filter
	displayGroups := allGroups
	header := fmt.Sprintf("📋 *%d grup* — ketik nomor atau nama:", len(allGroups))
	if caption != "" && !isNumericOnly(caption) {
		filtered := filterGroups(allGroups, caption)
		if len(filtered) == 1 {
			groupPendingMu.Lock()
			groupPending[user] = newPending
			groupPendingMu.Unlock()
			sendGroupStatus(ctx, evt, user, newPending, filtered[0])
			return
		} else if len(filtered) > 0 {
			displayGroups = filtered
			newPending.groups = filtered
			header = fmt.Sprintf("🔍 *%d grup* cocok:", len(filtered))
		}
	}

	groupPendingMu.Lock()
	groupPending[user] = newPending
	groupPendingMu.Unlock()

	reactMsg(ctx, evt, "✅")
	sendGroupList(ctx, chat, displayGroups, header)
}

// sendGroupStatus kirim sebagai WhatsApp Group Status (tab "Updates" di grup).
// Equivalent Baileys: conn.sendMessage(jid, { groupStatusMessage: payload })
//
// Syarat: bot harus menjadi admin grup agar bisa posting ke tab Updates.
func sendGroupStatus(ctx context.Context, evt *events.Message, user string, p *pendingGroupSend, target groupEntry) {
	chat := evt.Info.Chat

	groupPendingMu.Lock()
	delete(groupPending, user)
	groupPendingMu.Unlock()

	// Fix: anti double-send — dua pesan user berdekatan bisa baca pending yang
	// sama sebelum salah satunya delete dari map (lihat pendingGroupSend.mu).
	p.mu.Lock()
	if p.claimed {
		p.mu.Unlock()
		return
	}
	p.claimed = true
	p.mu.Unlock()

	reactMsg(ctx, evt, "⏳")

	// ── Cek apakah bot adalah admin di grup ini ───────────────────────────────
	groupInfo, err := waClient.GetGroupInfo(ctx, target.JID)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil info grup: "+err.Error())
		return
	}

	// Bot bisa punya beberapa format JID:
	// - nomor biasa  : "628xxx@s.whatsapp.net"
	// - LID          : "xxxxxxxxx@lid"
	// - dengan device: "628xxx:16@s.whatsapp.net"
	// Kita kumpulkan semua kemungkinan user-string lalu cek satu per satu.
	botUsers := map[string]bool{}
	if waClient.Store.ID != nil {
		botUsers[waClient.Store.ID.ToNonAD().User] = true
		botUsers[waClient.Store.ID.User] = true
	}
	if lid := waClient.Store.GetLID(); !lid.IsEmpty() {
		botUsers[lid.ToNonAD().User] = true
		botUsers[lid.User] = true
	}

	isAdmin := false
	for _, gp := range groupInfo.Participants {
		pUser := gp.JID.ToNonAD().User
		if botUsers[pUser] || botUsers[gp.JID.User] {
			isAdmin = gp.IsAdmin || gp.IsSuperAdmin
			break
		}
	}
	if !isAdmin {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Bot bukan admin di *%s*.\n\n"+
				"Tab *Updates* hanya bisa diposting oleh admin grup.\n"+
				"Jadikan bot admin dulu, lalu coba lagi.",
			target.Subject))
		return
	}

	typeLabel := "teks"
	if p.media != nil {
		typeLabel = p.media.mediaType
	}
	sendText(ctx, chat, fmt.Sprintf("⏳ Mengirim %s ke tab Updates *%s*...", typeLabel, target.Subject))

	// ── Build inner message ───────────────────────────────────────────────────
	var innerMsg *waE2E.Message
	if p.media == nil {
		// ExtendedTextMessage biar formatting rich tetap tampil di tab Updates.
		innerMsg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(p.caption)}}
	} else {
		innerMsg, err = buildInnerMsg(p.media)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal siapkan media: "+err.Error())
			return
		}
	}

	// ── Coba V2 dulu (format lebih baru), fallback ke V1 ─────────────────────
	wrapper := &waE2E.FutureProofMessage{Message: innerMsg}

	_, err = waClient.SendMessage(ctx, target.JID, &waE2E.Message{
		GroupStatusMessageV2: wrapper,
	})
	if err != nil {
		// Fallback ke V1 (format lama)
		pool.logger.Warn().Err(err).Str("group", target.Subject).Msg("swgc2: V2 gagal, coba V1")
		_, err = waClient.SendMessage(ctx, target.JID, &waE2E.Message{
			GroupStatusMessage: wrapper,
		})
	}

	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim group status: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ Berhasil posting ke tab *Updates* grup *%s*!", target.Subject))
}

// sendGroupList kirim daftar grup sebagai interactive single_select (list
// dropdown) — klik row = kirim command "swgc2 <index>" (diextract jadi teks
// biasa, diproses handleSWGC2 yang sama). Fallback berantai: interactive →
// ListMessage → teks biasa (kalau klien WA tidak support native flow).
func sendGroupList(ctx context.Context, chat types.JID, groups []groupEntry, header string) {
	const maxRowsPerSection = 10 // batas aman row per section di WA

	if len(groups) <= 50 {
		var sections []listSection
		for start := 0; start < len(groups); start += maxRowsPerSection {
			end := start + maxRowsPerSection
			if end > len(groups) {
				end = len(groups)
			}
			var rows []listRow
			for i := start; i < end; i++ {
				subject := groups[i].Subject
				if len(subject) > 24 { // batas judul row di WA
					subject = subject[:23] + "…"
				}
				rows = append(rows, listRow{
					id:    fmt.Sprintf("%sswgc2 %d", Prefix, i),
					title: subject,
					desc:  fmt.Sprintf("%d anggota", groups[i].MemberCount),
				})
			}
			sections = append(sections, listSection{
				title: fmt.Sprintf("Grup %d–%d", start+1, end),
				rows:  rows,
			})
		}

		b := NewMsgBuilder().
			SetHeader("📋 PILIH GRUP", fmt.Sprintf("%d grup — klik untuk pilih", len(groups))).
			SetBody(header).
			SetFooter("Kadaluarsa 3 menit").
			AddSelectButton("Pilih Grup", sections)
		if sendErr := b.Send(ctx, chat); sendErr == nil {
			return
		} else {
			pool.logger.Warn().Err(sendErr).Msg("sendGroupList: interactive gagal, coba ListMessage")
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
				Title:       proto.String("📋 PILIH GRUP"),
				Description: proto.String(header),
				ButtonText:  proto.String("Pilih Grup"),
				FooterText:  proto.String("Kadaluarsa 3 menit"),
				ListType:    waE2E.ListMessage_SINGLE_SELECT.Enum(),
				Sections:    lmSections,
			},
		}
		if _, sendErr := waClient.SendMessage(ctx, chat, listMsg); sendErr == nil {
			return
		} else {
			pool.logger.Warn().Err(sendErr).Msg("sendGroupList: ListMessage gagal, fallback teks")
		}
	}

	// Fallback 2: teks biasa (format lama, paling kompatibel).
	var b strings.Builder
	fmt.Fprintf(&b, "╔═ 『 📋 PILIH GRUP 』\n")
	fmt.Fprintf(&b, "║ %s\n", header)
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	for i, g := range groups {
		fmt.Fprintf(&b, "║ *[%d]* %s (%d anggota)\n", i, g.Subject, g.MemberCount)
	}
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	fmt.Fprintf(&b, "║ Ketik: *%sswgc2 0*  atau  *%sswgc2 nama grup*\n", Prefix, Prefix)
	fmt.Fprintf(&b, "║ _(Kadaluarsa 3 menit)_\n")
	fmt.Fprintf(&b, "╚══════════════════════════")
	sendText(ctx, chat, b.String())
}
