package main

// afk.go — !afk: tandai user sedang AFK (Away From Keyboard).
// Saat user AFK, siapa pun yang mention/tag dia di grup akan diberi tahu
// "user sedang AFK". Begitu user AFK mengirim pesan apa pun, status AFK
// otomatis dihapus dan dia diberi tahu sudah kembali.
//
// Nama user ditampilkan sebagai mention (@<nomor>) yang bisa diklik, bukan
// nomor telepon (+62xxx).
//
// State disimpan di memory (map) — tidak persist ke disk. Reset saat bot
// restart. Cukup untuk fitur AFK standar.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// afkEntry — satu status AFK per user.
type afkEntry struct {
	Reason string
	Since  time.Time
	Chat   types.JID // chat tempat user set AFK (untuk konteks)
	JID    types.JID // JID user yang AFK (untuk mention)
}

// afkState — map user (nomor string) → status AFK.
var afkState = struct {
	sync.RWMutex
	m map[string]*afkEntry
}{m: make(map[string]*afkEntry)}

// setAfk — tandai user AFK. Mengembalikan false kalau sudah AFK.
func setAfk(user string, reason string, chat types.JID, jid types.JID) bool {
	afkState.Lock()
	defer afkState.Unlock()
	if _, exists := afkState.m[user]; exists {
		return false
	}
	afkState.m[user] = &afkEntry{
		Reason: reason,
		Since:  time.Now(),
		Chat:   chat,
		JID:    jid.ToNonAD(),
	}
	return true
}

// clearAfk — hapus status AFK user. Mengembalikan entry yang dihapus (nil kalau tidak AFK).
func clearAfk(user string) *afkEntry {
	afkState.Lock()
	defer afkState.Unlock()
	e, exists := afkState.m[user]
	if !exists {
		return nil
	}
	delete(afkState.m, user)
	return e
}

// getAfk — ambil status AFK user (nil kalau tidak AFK).
func getAfk(user string) *afkEntry {
	afkState.RLock()
	defer afkState.RUnlock()
	return afkState.m[user]
}

// afkDuration — format durasi AFK (mis. "2 jam 5 menit").
func afkDuration(since time.Time) string {
	d := time.Since(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d detik", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d jam %d menit", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d hari %d jam", int(d.Hours()/24), int(d.Hours())%24)
	}
}

// afkMentionText — teks mention "@<nomor>" untuk JID user.
func afkMentionText(jid types.JID) string {
	return "@" + jid.ToNonAD().User
}

// handleAfk — command !afk <alasan>.
func handleAfk(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	user := senderUser(evt)
	reason := strings.TrimSpace(args)
	jid := evt.Info.Sender.ToNonAD()

	// Kalau user sudah AFK, !afk lagi = update alasan (atau tampilkan status).
	if e := getAfk(user); e != nil {
		if reason == "" {
			sendMention(ctx, chat, fmt.Sprintf(
				"╔═ 『 😴 AFK 』\n"+
					"║ Kamu sedang AFK.\n"+
					"╠══════════════════════════\n"+
					"║\n"+
					"║ 📝 Alasan: *%s*\n"+
					"║ ⏱️ Selama: *%s*\n"+
					"║\n"+
					"║ Ketik *%safk* lagi untuk update alasan,\n"+
					"║ atau kirim pesan apa pun untuk kembali.\n"+
					"╚══════════════════════════",
				e.Reason, afkDuration(e.Since), Prefix), jid)
			return
		}
		// Update alasan.
		afkState.Lock()
		afkState.m[user].Reason = reason
		afkState.m[user].Since = time.Now()
		afkState.Unlock()
		sendMention(ctx, chat, fmt.Sprintf(
			"╔═ 『 😴 AFK 』\n"+
				"║ Alasan AFK di-update.\n"+
				"╠══════════════════════════\n"+
				"║\n"+
				"║ 📝 Alasan: *%s*\n"+
				"║\n"+
				"║ Kirim pesan apa pun untuk kembali.\n"+
				"╚══════════════════════════", reason), jid)
		return
	}

	// Set AFK baru.
	if !setAfk(user, reason, chat, jid) {
		return
	}

	if reason == "" {
		reason = "tanpa alasan"
	}
	sendMention(ctx, chat, fmt.Sprintf(
		"╔═ 『 😴 AFK 』\n"+
			"║ %s sekarang AFK.\n"+
			"╠══════════════════════════\n"+
			"║\n"+
			"║ 📝 Alasan: *%s*\n"+
			"║\n"+
			"║ Siapa pun yang mention kamu akan diberi tahu.\n"+
			"║ Kirim pesan apa pun untuk kembali.\n"+
			"╚══════════════════════════", afkMentionText(jid), reason), jid)
}

// checkAfkReturn — cek apakah sender baru saja kembali dari AFK.
// Dipanggil untuk SEMUA pesan (command maupun non-command). Kalau sender
// sedang AFK, hapus status & beri tahu dia sudah kembali.
func checkAfkReturn(ctx context.Context, evt *events.Message) {
	if evt.Info.IsFromMe {
		return
	}
	user := senderUser(evt)
	e := clearAfk(user)
	if e == nil {
		return
	}
	jid := evt.Info.Sender.ToNonAD()
	sendMention(ctx, evt.Info.Chat, fmt.Sprintf(
		"╔═ 『 👋 SELAMAT DATANG KEMBALI 』\n"+
			"║ %s sudah kembali!\n"+
			"╠══════════════════════════\n"+
			"║\n"+
			"║ ⏱️ AFK selama: *%s*\n"+
			"║ 📝 Alasan: *%s*\n"+
			"╚══════════════════════════",
		afkMentionText(jid), afkDuration(e.Since), e.Reason), jid)
}

// checkAfkMention — cek apakah pesan mention user yang sedang AFK.
// Dipanggil untuk SEMUA pesan grup. Kalau ada mention ke user AFK,
// beri tahu pengirim bahwa user tsb sedang AFK. Status AFK TIDAK dihapus
// di sini — user AFK hanya "kembali" kalau dia sendiri mengirim pesan
// (diproses oleh checkAfkReturn).
func checkAfkMention(ctx context.Context, evt *events.Message) {
	if evt.Info.IsFromMe || !evt.Info.IsGroup {
		return
	}
	ci := msgContextInfo(evt)
	if ci == nil {
		return
	}
	mentioned := ci.GetMentionedJID()
	if len(mentioned) == 0 {
		return
	}

	// Sender sendiri (jangan kasih tahu kalau dia mention dirinya sendiri).
	sender := senderUser(evt)

	for _, m := range mentioned {
		jid, err := types.ParseJID(m)
		if err != nil {
			continue
		}
		user := jid.ToNonAD().User
		if user == "" || user == sender {
			continue
		}
		e := getAfk(user)
		if e == nil {
			continue
		}
		// User ini AFK — beri tahu pengirim bahwa user tsb sedang AFK.
		mentionJID := jid.ToNonAD()
		msg := fmt.Sprintf(
			"😴 %s sedang AFK\n"+
				"║ 📝 Alasan: *%s*\n"+
				"║ ⏱️ Sejak: *%s*",
			afkMentionText(mentionJID), e.Reason, afkDuration(e.Since))
		sendMention(ctx, evt.Info.Chat, msg, mentionJID)
	}
}
