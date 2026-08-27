package main

// group_manage.go — manajemen grup dasar (port dari Ourin/plugins/group &
// Anya MD-Update): close/open, kick, add, promote/demote, tagall, setname,
// setdesc, setppgc, warn/resetwarn. Semua command admin-grup (owner bot
// juga boleh). Semua pakai API bawaan whatsmeow (group.go).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// requireGroupAdminOwner — wajib admin grup atau owner bot. Admin grup
// memakai command untuk grupnya sendiri; owner bot bisa di mana saja.
// Balas pesan error & return false kalau tidak boleh.
func requireGroupAdminOwner(ctx context.Context, evt *events.Message) (types.JID, bool) {
	chat := evt.Info.Chat
	if chat.Server != types.GroupServer {
		sendText(ctx, chat, "❌ Perintah ini hanya bisa dipakai di dalam grup.")
		return chat, false
	}
	if evt.Info.IsFromMe {
		return chat, true
	}
	if !isGroupAdminOrOwner(ctx, evt) {
		sendText(ctx, chat, "❌ Hanya admin grup atau owner bot yang bisa memakai perintah ini.")
		return chat, false
	}
	return chat, true
}

// resolveGroupMember cari target member: reply (participant) atau nomor
// langsung. LID di-resolve ke nomor asli (untuk kick/promote/demote).
func resolveGroupMember(ctx context.Context, evt *events.Message, args string) (types.JID, error) {
	ctxInfo := msgContextInfo(evt)
	if q := ctxInfo.GetParticipant(); q != "" {
		jid, err := types.ParseJID(normalizeJIDString(q))
		if err != nil {
			return types.JID{}, err
		}
		return resolveLIDToPhone(ctx, jid)
	}
	// Tag/mention langsung (@orang) — tanpa perlu reply atau nomor manual.
	if mentions := ctxInfo.GetMentionedJID(); len(mentions) > 0 {
		jid, err := types.ParseJID(normalizeJIDString(mentions[0]))
		if err != nil {
			return types.JID{}, err
		}
		return resolveLIDToPhone(ctx, jid)
	}
	number := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "@"))
	number = strings.NewReplacer(" ", "", "-", "", "+", "").Replace(number)
	if number == "" {
		return types.JID{}, fmt.Errorf("reply pesan target, tag orangnya, atau tulis nomornya")
	}
	return types.ParseJID(number + "@s.whatsapp.net")
}

// resolveGroupMembers versi multi-target: reply/tag = 1 target, plus semua
// nomor yang ditulis di args (dipisah spasi). Dipakai !kick / !add.
func resolveGroupMembers(ctx context.Context, evt *events.Message, args string) ([]types.JID, error) {
	var out []types.JID
	ctxInfo := msgContextInfo(evt)
	if q := ctxInfo.GetParticipant(); q != "" {
		jid, err := types.ParseJID(normalizeJIDString(q))
		if err == nil {
			r, err := resolveLIDToPhone(ctx, jid)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	} else if mentions := ctxInfo.GetMentionedJID(); len(mentions) > 0 {
		jid, err := types.ParseJID(normalizeJIDString(mentions[0]))
		if err == nil {
			r, err := resolveLIDToPhone(ctx, jid)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	}
	for _, tok := range strings.Fields(args) {
		num := strings.TrimPrefix(strings.TrimSpace(tok), "@")
		num = strings.NewReplacer(" ", "", "-", "", "+", "").Replace(num)
		if num == "" {
			continue
		}
		jid, err := types.ParseJID(num + "@s.whatsapp.net")
		if err != nil {
			return nil, fmt.Errorf("nomor tidak valid: %s", tok)
		}
		out = append(out, jid)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("reply pesan target, tag orangnya, atau tulis nomornya")
	}
	return out, nil
}

// filterKickTargets buang target yang tidak boleh di-kick: bot sendiri,
// owner bot, dan admin grup. Return daftar yang aman + alasan yang ditolak.
func filterKickTargets(ctx context.Context, chat types.JID, targets []types.JID) ([]types.JID, []string) {
	var ok []types.JID
	var rejected []string
	botUser := ""
	if waClient.Store.ID != nil {
		botUser = waClient.Store.ID.ToNonAD().User
	}
	admins := groupAdminSet(ctx, chat)
	for _, t := range targets {
		u := t.ToNonAD().User
		switch {
		case u == botUser:
			rejected = append(rejected, "bot sendiri")
		case isOwner(u) || isOwnerDB(u):
			rejected = append(rejected, "owner bot")
		case admins != nil && admins[u]:
			rejected = append(rejected, "admin grup (demote dulu kalau mau dikeluarkan)")
		default:
			ok = append(ok, t)
		}
	}
	return ok, rejected
}

// ─── Close / Open ────────────────────────────────────────────────────────────

func handleGroupClose(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	if err := waClient.SetGroupAnnounce(ctx, chat, true); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengunci grup: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔒 Grup dikunci — hanya admin yang bisa kirim pesan.")
}

func handleGroupOpen(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	if err := waClient.SetGroupAnnounce(ctx, chat, false); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal membuka grup: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔓 Grup dibuka — semua member bisa kirim pesan.")
}

// ─── Kick / Add ──────────────────────────────────────────────────────────────

func handleGroupKick(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	targets, err := resolveGroupMembers(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	targets, rejected := filterKickTargets(ctx, chat, targets)
	if len(targets) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak ada yang bisa dikeluarkan: "+strings.Join(rejected, ", ")+".")
		return
	}
	reactMsg(ctx, evt, "⏳")
	if _, err := waClient.UpdateGroupParticipants(ctx, chat, targets, whatsmeow.ParticipantChangeRemove); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kick: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	msg := fmt.Sprintf("👢 %d member dikeluarkan dari grup.", len(targets))
	if len(rejected) > 0 {
		msg += "\nDilewati: " + strings.Join(rejected, ", ") + "."
	}
	sendText(ctx, chat, msg)
}

func handleGroupAdd(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	// Support reply/tag orangnya langsung — tidak wajib ketik nomor manual.
	targets, err := resolveGroupMembers(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sadd 628xxx 628yyy`, atau reply/tag orang yang mau ditambah.", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	if _, err := waClient.UpdateGroupParticipants(ctx, chat, targets, whatsmeow.ParticipantChangeAdd); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menambah: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ %d member ditambahkan ke grup.", len(targets)))
}

// ─── Promote / Demote ────────────────────────────────────────────────────────

func handleGroupPromote(ctx context.Context, evt *events.Message, args string, demote bool) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	target, err := resolveGroupMember(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	action := whatsmeow.ParticipantChangePromote
	verb := "diangkat jadi admin"
	if demote {
		action = whatsmeow.ParticipantChangeDemote
		verb = "diturunkan dari admin"
	}
	reactMsg(ctx, evt, "⏳")
	if _, err := waClient.UpdateGroupParticipants(ctx, chat, []types.JID{target}, action); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("✅ *%s* %s.", displayUser(ctx, target), verb))
}

// ─── Tagall ──────────────────────────────────────────────────────────────────

// groupMemberJIDs ambil daftar JID semua member (tanpa bot sendiri).
func groupMemberJIDs(ctx context.Context, chat types.JID) ([]types.JID, string, error) {
	info, err := waClient.GetGroupInfo(ctx, chat)
	if err != nil {
		return nil, "", err
	}
	var mentions []types.JID
	botUser := ""
	if waClient.Store.ID != nil {
		botUser = waClient.Store.ID.ToNonAD().User
	}
	for _, p := range info.Participants {
		if p.JID.ToNonAD().User == botUser {
			continue // jangan tag bot sendiri
		}
		mentions = append(mentions, p.JID)
	}
	return mentions, info.Name, nil
}

func handleTagAll(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	mentions, groupName, err := groupMemberJIDs(ctx, chat)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar member: "+err.Error())
		return
	}
	text := args
	if text == "" {
		text = fmt.Sprintf("📢 *TAG ALL* — %s (%d member)", groupName, len(mentions))
	}
	if err := sendMentionMany(ctx, chat, text, mentions); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim tagall: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// handleHideTag kirim pesan + mention semua member (tanpa teks tagall).
func handleHideTag(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	mentions, _, err := groupMemberJIDs(ctx, chat)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar member: "+err.Error())
		return
	}
	text := strings.TrimSpace(args)
	if text == "" {
		text = "📢"
	}
	if err := sendMentionMany(ctx, chat, text, mentions); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Set name / desc / pp ────────────────────────────────────────────────────

func handleGroupSetName(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	name := strings.TrimSpace(args)
	if name == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%ssetname Nama Grup Baru`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	if err := waClient.SetGroupName(ctx, chat, name); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ganti nama: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ Nama grup diubah menjadi *%s*", name))
}

func handleGroupSetDesc(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	desc := strings.TrimSpace(args)
	if desc == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%ssetdesc Deskripsi baru`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	if err := waClient.SetGroupTopic(ctx, chat, "", "", desc); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ganti deskripsi: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "✅ Deskripsi grup diperbarui.")
}

// convertToJPEG — konversi data gambar apapun (WebP, PNG, HEIC, dll) ke JPEG
// menggunakan ffmpeg. Dibutuhkan oleh SetGroupPhoto yang hanya menerima JPEG.
// Kalau data sudah JPEG, langsung return tanpa konversi.
func convertToJPEG(data []byte) ([]byte, error) {
	// Fast path: data sudah JPEG (magic FFD8FF) → langsung pakai.
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return data, nil
	}

	in, err := os.CreateTemp("", "pp_in_*")
	if err != nil {
		return nil, fmt.Errorf("create temp input: %w", err)
	}
	inName := in.Name()
	defer os.Remove(inName)
	if _, err := in.Write(data); err != nil {
		in.Close()
		return nil, err
	}
	in.Close()

	outName := inName + ".jpg"
	defer os.Remove(outName)

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", inName,
		"-vf", "scale=640:640:force_original_aspect_ratio=decrease",
		"-pix_fmt", "yuvj420p",
		"-q:v", "2",
		"-f", "image2", outName)
	cmd.Stderr = &stderr
	if err := runCmdTimeout(cmd, 15*time.Second); err != nil {
		return nil, fmt.Errorf("ffmpeg: %s", strings.TrimSpace(stderr.String()))
	}
	jpegData, err := os.ReadFile(outName)
	if err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}
	if len(jpegData) < 3 || jpegData[0] != 0xFF || jpegData[1] != 0xD8 || jpegData[2] != 0xFF {
		return nil, fmt.Errorf("ffmpeg menghasilkan data bukan JPEG (%d bytes)", len(jpegData))
	}
	return jpegData, nil
}

func handleGroupSetPP(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	med, err := detectMedia(ctx, evt)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	if med == nil || med.data == nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu: `%ssetppgc`", Prefix))
		return
	}
	if med.mediaType != "image" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Yang direply *%s*, bukan gambar.\n> Reply *foto* dulu: `%ssetppgc`",
			med.mediaType, Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Konversi ke JPEG dulu — SetGroupPhoto hanya menerima JPEG.
	jpegData, err := convertToJPEG(med.data)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal konversi gambar: "+err.Error())
		return
	}
	if _, err := waClient.SetGroupPhoto(ctx, chat, jpegData); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ganti foto grup: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "✅ Foto grup diperbarui.")
}

// ─── Warn system ─────────────────────────────────────────────────────────────
// In-memory: grup → user → jumlah warn. Warn ke-3 → otomatis kick + reset.
// Port sederhana dari Ourin warn.js / resetwarn.js.

const warnLimit = 3

var warnState = struct {
	sync.Mutex
	m map[string]map[string]int // group key → user key → warn count
}{m: map[string]map[string]int{}}

func handleWarn(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	target, err := resolveGroupMember(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	// Jangan warn bot sendiri / owner bot / admin grup.
	if _, rejected := filterKickTargets(ctx, chat, []types.JID{target}); len(rejected) > 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak bisa warn: "+strings.Join(rejected, ", ")+".")
		return
	}
	groupKey := chat.ToNonAD().String()
	userKey := target.ToNonAD().User

	ensureGroupState()
	warnState.Lock()
	if warnState.m[groupKey] == nil {
		warnState.m[groupKey] = map[string]int{}
	}
	warnState.m[groupKey][userKey]++
	count := warnState.m[groupKey][userKey]
	warnState.Unlock()
	saveGroupState()

	msg := fmt.Sprintf("⚠️ *@%s* mendapat warn *%d/%d*.", userKey, count, warnLimit)
	if count >= warnLimit {
		msg += "\nBatas warn tercapai — keluar dari grup."
	}
	if err := sendMention(ctx, chat, msg, target); err != nil {
		pool.logger.Warn().Err(err).Msg("warn: gagal kirim mention")
	}

	if count >= warnLimit {
		warnState.Lock()
		delete(warnState.m[groupKey], userKey)
		warnState.Unlock()
		saveGroupState()
		reactMsg(ctx, evt, "⏳")
		if _, err := waClient.UpdateGroupParticipants(ctx, chat, []types.JID{target}, whatsmeow.ParticipantChangeRemove); err != nil {
			pool.logger.Warn().Err(err).Msg("warn: kick otomatis gagal")
		}
		return
	}
	reactMsg(ctx, evt, "✅")
}

func handleResetWarn(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	groupKey := chat.ToNonAD().String()
	arg := strings.ToLower(strings.TrimSpace(args))
	if arg == "" || arg == "all" || arg == "semua" {
		ensureGroupState()
		warnState.Lock()
		delete(warnState.m, groupKey)
		warnState.Unlock()
		saveGroupState()
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "✅ Semua warn di grup ini direset.")
		return
	}
	target, err := resolveGroupMember(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	userKey := target.ToNonAD().User
	ensureGroupState()
	warnState.Lock()
	delete(warnState.m[groupKey], userKey)
	warnState.Unlock()
	saveGroupState()
	reactMsg(ctx, evt, "✅")
	if err := sendMention(ctx, chat, fmt.Sprintf("✅ Warn *@%s* direset.", userKey), target); err != nil {
		sendText(ctx, chat, fmt.Sprintf("✅ Warn @%s direset.", userKey))
	}
}

// handleWarnList tampilkan daftar warn semua member di grup.
func handleWarnList(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	groupKey := chat.ToNonAD().String()
	ensureGroupState()
	warnState.Lock()
	m := warnState.m[groupKey]
	lines := make([]string, 0, len(m))
	for userKey, count := range m {
		lines = append(lines, fmt.Sprintf("• @%s — *%d/%d*", userKey, count, warnLimit))
	}
	warnState.Unlock()
	if len(lines) == 0 {
		sendText(ctx, chat, "✅ Tidak ada member yang kena warn di grup ini.")
		return
	}
	// Sort biar urutannya stabil (map acak).
	sort.Strings(lines)
	// Mention JID supaya @user di teks jadi highlight/klikabel.
	var mentionJIDs []types.JID
	for userKey := range m {
		if jid, err := types.ParseJID(userKey + "@s.whatsapp.net"); err == nil {
			mentionJIDs = append(mentionJIDs, jid)
		}
	}
	text := "⚠️ *DAFTAR WARN* — grup ini\n\n" + strings.Join(lines, "\n") +
		fmt.Sprintf("\n\nBatas warn: *%d* (otomatis kick). Reset: `%sresetwarn all`", warnLimit, Prefix)
	if err := sendMentionMany(ctx, chat, text, mentionJIDs); err != nil {
		sendText(ctx, chat, text)
	}
}

// ─── Link undangan / info grup / keluar ──────────────────────────────────────

// handleGroupLink ambil link undangan grup (reset=false).
func handleGroupLink(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	link, err := waClient.GetGroupInviteLink(ctx, chat, false)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil link undangan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔗 *Link undangan grup:*\n"+link)
}

// handleGroupRevoke reset link undangan lama & buat yang baru.
func handleGroupRevoke(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	link, err := waClient.GetGroupInviteLink(ctx, chat, true)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal reset link undangan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "🔄 Link undangan lama *tidak berlaku lagi*.\n🔗 Link baru:\n"+link)
}

// handleGroupInfo tampilkan info grup: nama, ID, owner, dibuat, member, admin.
func handleGroupInfo(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	info, err := waClient.GetGroupInfo(ctx, chat)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil info grup: "+err.Error())
		return
	}
	owner := "-"
	if !info.OwnerJID.IsEmpty() {
		owner = displayUser(ctx, info.OwnerJID)
	}
	created := "-"
	if !info.GroupCreated.IsZero() {
		created = info.GroupCreated.Format("02 Jan 2006")
	}
	adminCount := 0
	for _, p := range info.Participants {
		if p.IsAdmin || p.IsSuperAdmin {
			adminCount++
		}
	}
	memberCount := info.ParticipantCount
	if memberCount == 0 {
		memberCount = len(info.Participants)
	}
	desc := strings.TrimSpace(info.Topic)
	if len(desc) > 200 {
		desc = desc[:200] + "…"
	}
	msg := fmt.Sprintf(
		"ℹ️ *INFO GRUP*\n\n"+
			"📛 Nama: *%s*\n"+
			"🆔 ID: `%s`\n"+
			"👑 Owner: %s\n"+
			"📅 Dibuat: %s\n"+
			"👥 Member: *%d* (admin: %d)\n",
		info.Name, chat.ToNonAD().String(), owner, created, memberCount, adminCount)
	if desc != "" {
		msg += "\n📝 Deskripsi:\n" + desc
	}
	sendText(ctx, chat, msg)
}

// handleGroupOut bot keluar dari grup.
func handleGroupOut(ctx context.Context, evt *events.Message, args string) {
	chat, ok := requireGroupAdminOwner(ctx, evt)
	if !ok {
		return
	}
	reactMsg(ctx, evt, "👋")
	sendText(ctx, chat, "👋 Bot keluar dari grup. Sampai jumpa!")
	if err := waClient.LeaveGroup(ctx, chat); err != nil {
		pool.logger.Warn().Err(err).Str("group", chat.String()).Msg("out: gagal keluar grup")
	}
}

// helper yang dipakai tagall — mention banyak JID sekaligus.
func sendMentionMany(ctx context.Context, chat types.JID, text string, mentions []types.JID) error {
	// WA membatasi mention (~500 per pesan); potong kalau lebih.
	if len(mentions) > 500 {
		mentions = mentions[:500]
	}
	mentionStrs := make([]string, 0, len(mentions))
	for _, m := range mentions {
		mentionStrs = append(mentionStrs, m.ToNonAD().String())
	}
	_, err := waClient.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: mentionStrs,
			},
		},
	})
	return err
}
