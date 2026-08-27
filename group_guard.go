package main

// group_guard.go — fitur "jaga grup": antilink, antitoxic, welcome.
//
// Config in-memory per grup (reset saat bot restart). Hanya admin grup
// (atau owner bot) yang bisa mengubah config.
//
// Antilink  : pesan non-admin berisi link (http/https, chat.whatsapp.com,
//             t.me, wa.me) → peringatan dengan mention pengirim.
// Antitoxic : pesan non-admin berisi kata kasar → peringatan + mention.
// Welcome   : sambut member baru yang join grup.
//
// Catatan: pesan orang lain TIDAK bisa dihapus lewat protokol WhatsApp
// (delete/revoke hanya untuk pesan sendiri), jadi moderasi berupa
// peringatan, bukan penghapusan.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// groupGuard — status fitur jaga grup.
type groupGuard struct {
	Antilink   bool
	Antitoxic  bool
	Welcome    bool
	WelcomeMsg string // pesan welcome custom ("" = default)
}

var (
	guardMu sync.Mutex
	guards  = map[string]*groupGuard{} // key: chat.ToNonAD().String()
)

// reAnyLink — link umum + link undangan grup (whatsapp/t.me/wa.me).
var reAnyLink = regexp.MustCompile(`(?i)(https?://[^\s]+|chat\.whatsapp\.com/\S+|t\.me/[^\s]+|wa\.me/[^\s]+)`)

// toxicWords — kata kasar umum (bahasa Indonesia, konteks grup WA).
// Sederhana & mudah ditambah; false-positive (mis. "babi" bercanda) adalah
// trade-off yang diterima untuk fitur sederhana ini.
var toxicWords = []string{
	"anjing", "bangsat", "babi", "kontol", "memek", "ngentot", "jancok",
	"asu", "kampang", "setan", "keparat", "pantek", "perek", "meki",
	"geblek", "goblok", "tolol", "bejat", "sinting", "dongo",
}

// guardSnapshot ambil salinan config grup (aman dibaca dari goroutine mana pun).
func guardSnapshot(chat types.JID) groupGuard {
	ensureGroupState()
	guardMu.Lock()
	defer guardMu.Unlock()
	if g := guards[chat.ToNonAD().String()]; g != nil {
		return *g
	}
	return groupGuard{}
}

// guardSet ubah config grup dalam lock (mutasi via closure) + persist.
func guardSet(chat types.JID, mutate func(*groupGuard)) {
	ensureGroupState()
	guardMu.Lock()
	key := chat.ToNonAD().String()
	g := guards[key]
	if g == nil {
		g = &groupGuard{}
		guards[key] = g
	}
	mutate(g)
	guardMu.Unlock()
	saveGroupState()
}

// ─── Admin cache ─────────────────────────────────────────────────────────────
// GetGroupInfo per pesan itu mahal (network call). Simpan set admin per grup
// dengan TTL 1 menit — cukup akurat untuk moderasi & command.

type adminCacheEntry struct {
	admins map[string]bool // key: user part JID (ToNonAD().User)
	ts     time.Time
}

var adminCache = struct {
	sync.Mutex
	m map[string]adminCacheEntry
}{m: map[string]adminCacheEntry{}}

// groupAdminSet ambil (dengan cache) set admin grup. Return nil kalau gagal.
func groupAdminSet(ctx context.Context, chat types.JID) map[string]bool {
	key := chat.ToNonAD().String()
	adminCache.Lock()
	if e, ok := adminCache.m[key]; ok && time.Since(e.ts) < time.Minute {
		adminCache.Unlock()
		return e.admins
	}
	adminCache.Unlock()

	info, err := waClient.GetGroupInfo(ctx, chat)
	if err != nil {
		pool.logger.Warn().Err(err).Str("group", key).Msg("guard: gagal ambil info grup")
		return nil
	}
	set := map[string]bool{}
	for _, p := range info.Participants {
		if p.IsAdmin || p.IsSuperAdmin {
			set[p.JID.ToNonAD().User] = true
		}
	}
	adminCache.Lock()
	adminCache.m[key] = adminCacheEntry{admins: set, ts: time.Now()}
	adminCache.Unlock()
	return set
}

// isGroupAdminUser — apakah user adalah admin/superadmin di grup chat.
func isGroupAdminUser(ctx context.Context, chat types.JID, user types.JID) bool {
	set := groupAdminSet(ctx, chat)
	if set == nil {
		return false
	}
	return set[user.ToNonAD().User]
}

// ─── Command: !antilink / !antitoxic / !welcome / !jaga ─────────────────────

// guardFeatureSet ubah satu fitur ("" = semua sekaligus untuk !jaga).
func guardFeatureSet(g *groupGuard, feature string, on bool) {
	apply := func(f string) {
		switch f {
		case "antilink":
			g.Antilink = on
		case "antitoxic":
			g.Antitoxic = on
		case "welcome":
			g.Welcome = on
		}
	}
	if feature == "" {
		apply("antilink")
		apply("antitoxic")
		apply("welcome")
		return
	}
	apply(feature)
}

// handleGuardCmd ubah/tampilkan status satu fitur jaga grup.
// Admin grup atau owner bot.
func handleGuardCmd(ctx context.Context, evt *events.Message, args, feature string) {
	chat := evt.Info.Chat
	if chat.Server != types.GroupServer {
		sendText(ctx, chat, "❌ Fitur jaga grup hanya bisa dipakai di dalam grup.")
		return
	}
	if evt.Info.IsFromMe {
		return
	}
	if !isGroupAdminOrOwner(ctx, evt) {
		sendText(ctx, chat, "❌ Hanya admin grup atau owner bot yang bisa mengubah pengaturan jaga grup.")
		return
	}

	labels := map[string]string{
		"antilink":  "🔗 Antilink",
		"antitoxic": "🚫 Antitoxic",
		"welcome":   "🎉 Welcome",
	}
	label := labels[feature]
	cmd := fmt.Sprintf("%s%s", Prefix, feature)

	switch strings.ToLower(strings.TrimSpace(args)) {
	case "on", "1", "true", "yes", "aktif":
		guardSet(chat, func(g *groupGuard) { guardFeatureSet(g, feature, true) })
		reactMsg(ctx, evt, "✅")
		if feature == "" {
			sendText(ctx, chat, "🛡️ Semua proteksi jaga grup: *ON* ✅")
		} else {
			sendText(ctx, chat, fmt.Sprintf("%s: *ON* ✅", label))
		}
	case "off", "0", "false", "no", "mati":
		guardSet(chat, func(g *groupGuard) { guardFeatureSet(g, feature, false) })
		reactMsg(ctx, evt, "✅")
		if feature == "" {
			sendText(ctx, chat, "🛡️ Semua proteksi jaga grup: *OFF*")
		} else {
			sendText(ctx, chat, fmt.Sprintf("%s: *OFF*", label))
		}
	default:
		s := guardSnapshot(chat)
		if feature == "" {
			sendText(ctx, chat, fmt.Sprintf(
				"🛡️ *JAGA GRUP* — grup ini\n\n"+
					"🔗 Antilink : *%s*\n"+
					"🚫 Antitoxic: *%s*\n"+
					"🎉 Welcome  : *%s*\n\n"+
					"Ubah dengan:\n"+
					"• `%sjaga on` / `%sjaga off` — semua sekaligus\n"+
					"• `%santilink on/off`\n"+
					"• `%santitoxic on/off`\n"+
					"• `%swelcome on/off`",
				onOff(s.Antilink), onOff(s.Antitoxic), onOff(s.Welcome),
				Prefix, Prefix, Prefix, Prefix, Prefix))
			return
		}
		sendText(ctx, chat, fmt.Sprintf(
			"%s: *%s*\n\nUbah dengan `%s on` / `%s off`",
			label, onOff(statusOf(&s, feature)), cmd, cmd))
	}
}

// statusOf ambil status satu fitur dari snapshot (untuk pesan status per-fitur).
func statusOf(g *groupGuard, feature string) bool {
	switch feature {
	case "antilink":
		return g.Antilink
	case "antitoxic":
		return g.Antitoxic
	case "welcome":
		return g.Welcome
	}
	return false
}

// handleSetWelcome simpan pesan welcome custom untuk grup.
// Placeholder: @user → mention member baru, @group → nama grup.
// Tanpa args = reset ke pesan default.
func handleSetWelcome(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	if chat.Server != types.GroupServer {
		sendText(ctx, chat, "❌ Fitur ini hanya bisa dipakai di dalam grup.")
		return
	}
	if evt.Info.IsFromMe {
		return
	}
	if !isGroupAdminOrOwner(ctx, evt) {
		sendText(ctx, chat, "❌ Hanya admin grup atau owner bot yang bisa mengubah pesan welcome.")
		return
	}
	msg := strings.TrimSpace(args)
	if msg == "" {
		guardSet(chat, func(g *groupGuard) { g.WelcomeMsg = "" })
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "✅ Pesan welcome dikembalikan ke default.")
		return
	}
	if len(msg) > 500 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Pesan welcome maksimal 500 karakter.")
		return
	}
	guardSet(chat, func(g *groupGuard) { g.WelcomeMsg = msg })
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, "✅ Pesan welcome disimpan.\n\n"+
		"Placeholder: `@user` = mention member baru, `@group` = nama grup.\n"+
		"Contoh: `"+Prefix+"setwelcome Halo @user, selamat datang di @group!`")
}

func onOff(v bool) string {
	if v {
		return "ON ✅"
	}
	return "OFF"
}

// ─── Scan pesan grup ─────────────────────────────────────────────────────────

// scanGroupGuard moderasi pesan grup sesuai config (antilink/antitoxic).
// Dipanggil dari handleMessage untuk SEMUA pesan grup — non-admin saja.
func scanGroupGuard(ctx context.Context, evt *events.Message, text string) {
	if !evt.Info.IsGroup || evt.Info.IsFromMe || text == "" {
		return
	}
	g := guardSnapshot(evt.Info.Chat)
	if !g.Antilink && !g.Antitoxic {
		return
	}
	// Admin & owner bebas dari moderasi.
	user := senderUser(evt)
	if isOwner(user) || isOwnerDB(user) {
		return
	}
	admins := groupAdminSet(ctx, evt.Info.Chat)
	if admins == nil {
		return // tidak tahu siapa admin → jangan risiko salah tindak
	}
	if admins[evt.Info.Sender.ToNonAD().User] {
		return
	}

	var reasons []string
	if g.Antilink && reAnyLink.MatchString(text) {
		reasons = append(reasons, "link")
	}
	if g.Antitoxic {
		lower := strings.ToLower(text)
		for _, w := range toxicWords {
			if strings.Contains(lower, w) {
				reasons = append(reasons, "kata tidak pantas")
				break
			}
		}
	}
	if len(reasons) == 0 {
		return
	}

	msg := "⚠️ *" + strings.Join(reasons, " & ") + " terdeteksi!*\n" +
		"Hindari " + strings.Join(reasons, " dan ") + " di grup ini ya 🙏"
	if err := sendMention(ctx, evt.Info.Chat, msg, evt.Info.Sender); err != nil {
		pool.logger.Warn().Err(err).Msg("guard: gagal kirim peringatan")
	}
}

// isGroupAdminOrOwner — apakah pengirim pesan admin grup atau owner bot.
// Sender LID di-resolve dulu supaya cocok dengan set admin (PN).
func isGroupAdminOrOwner(ctx context.Context, evt *events.Message) bool {
	if isOwner(senderUser(evt)) || isOwnerDB(senderUser(evt)) {
		return true
	}
	sender, err := resolveLIDToPhone(ctx, evt.Info.Sender)
	if err != nil {
		sender = evt.Info.Sender
	}
	return isGroupAdminUser(ctx, evt.Info.Chat, sender)
}

// ─── Welcome ─────────────────────────────────────────────────────────────────

// handleGroupJoin sambut member baru (events.GroupInfo dengan Join).
func handleGroupJoin(ctx context.Context, evt *events.GroupInfo) {
	if evt.JID.Server != types.GroupServer || len(evt.Join) == 0 {
		return
	}
	g := guardSnapshot(evt.JID)
	if !g.Welcome {
		return
	}
	groupName := ""
	if evt.Name != nil {
		groupName = evt.Name.Name
	}
	botUser := ""
	if waClient.Store.ID != nil {
		botUser = waClient.Store.ID.ToNonAD().User
	}
	for _, m := range evt.Join {
		mu := m.ToNonAD().User
		if mu == botUser {
			continue // bot sendiri masuk — tidak perlu menyambut diri sendiri
		}
		msg := welcomeMessage(g.WelcomeMsg, mu, groupName)
		if err := sendMention(ctx, evt.JID, msg, m); err != nil {
			pool.logger.Warn().Err(err).Msg("welcome: gagal kirim sambutan")
		}
	}
}

// welcomeMessage susun pesan welcome: custom (dengan placeholder) atau default.
func welcomeMessage(custom, user, groupName string) string {
	if custom != "" {
		msg := strings.ReplaceAll(custom, "@user", "@"+user)
		msg = strings.ReplaceAll(msg, "@group", groupName)
		return msg
	}
	msg := fmt.Sprintf("🎉 Selamat datang @%s!", user)
	if groupName != "" {
		msg += fmt.Sprintf("\nSelamat bergabung di *%s* 🎊\nBaca deskripsi & patuhi aturan grup ya.", groupName)
	}
	return msg
}

// sendMention kirim teks dengan mention satu user.
func sendMention(ctx context.Context, chat types.JID, text string, mention types.JID) error {
	_, err := waClient.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(text),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: []string{mention.ToNonAD().String()},
			},
		},
	})
	return err
}
