package main

// blacklist.go — !bl (blacklist user & grup) & !clear (bersihkan cache temp).
// Keduanya owner-only.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// isBlocked — sender ATAU grup tempat command dijalankan masuk blacklist
// (database/blacklist.json) → semua command ditolak. Owner selalu lolos.
// Satu kali baca DB untuk kedua cek.
func isBlocked(evt *events.Message) bool {
	sender := senderUser(evt)
	if isOwnerDB(sender) {
		return false
	}
	list := readNumbersDB(BlacklistPath)
	if containsNumber(list, sender) {
		return true
	}
	return evt.Info.Chat.Server == types.GroupServer && containsNumber(list, evt.Info.Chat.String())
}

// handleBlacklist — !bl add <target> / !bl del <target> / !bl list / !bl (panduan).
// Target: nomor 628xxx, atau grup — jalankan `!bl add` TANPA argumen di dalam
// grup yang mau diblokir (entry tersimpan sebagai "xxx@g.us").
func handleBlacklist(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) == 0 {
		sendText(ctx, chat, fmt.Sprintf(
			"🚫 *Blacklist*\n\n"+
				"> Blokir user/grup supaya tidak bisa pakai bot\n\n"+
				"*Format:*\n"+
				"> `%sbl add 628xxx` — blokir nomor\n"+
				"> `%sbl add` (di dalam grup) — blokir grup ini\n"+
				"> `%sbl del 628xxx` / `%sbl del` (di dalam grup) — buka blokir\n"+
				"> `%sbl list` — lihat daftar",
			Prefix, Prefix, Prefix, Prefix, Prefix))
		return
	}

	switch strings.ToLower(fields[0]) {
	case "add":
		target := blTarget(evt, strings.Join(fields[1:], " "))
		if target == "" {
			sendText(ctx, chat, "❌ Format: `!bl add 628xxx` — atau jalankan `!bl add` di dalam grup yang mau diblokir.")
			return
		}
		if isOwnerDB(target) {
			sendText(ctx, chat, "❌ "+blLabel(target)+" adalah owner — tidak bisa diblacklist.")
			return
		}
		if err := addBlacklistDB(target); err != nil {
			sendText(ctx, chat, "❌ Gagal simpan: "+err.Error())
			return
		}
		sendText(ctx, chat, "🚫 *"+blLabel(target)+"* diblacklist — semua command-nya ditolak.")

	case "del", "remove", "unbl":
		target := blTarget(evt, strings.Join(fields[1:], " "))
		if target == "" {
			sendText(ctx, chat, "❌ Format: `!bl del 628xxx` — atau jalankan `!bl del` di dalam grup yang mau dibuka.")
			return
		}
		if err := removeBlacklistDB(target); err != nil {
			sendText(ctx, chat, "❌ Gagal simpan: "+err.Error())
			return
		}
		sendText(ctx, chat, "✅ *"+blLabel(target)+"* dihapus dari blacklist — bisa pakai bot lagi.")

	case "list", "ls":
		list := readNumbersDB(BlacklistPath)
		if len(list) == 0 {
			sendText(ctx, chat, "📭 Blacklist kosong — tidak ada user/grup yang diblokir.")
			return
		}
		var users, groups []string
		for _, n := range list {
			if strings.HasSuffix(n, "@g.us") {
				groups = append(groups, blLabel(n))
			} else {
				users = append(users, n)
			}
		}
		var b strings.Builder
		b.WriteString("🚫 *Daftar Blacklist*\n\n")
		if len(users) > 0 {
			b.WriteString("_User:_\n")
			for i, n := range users {
				fmt.Fprintf(&b, "%d. `%s`\n", i+1, n)
			}
		}
		if len(groups) > 0 {
			b.WriteString("\n_Grup:_\n")
			for i, n := range groups {
				fmt.Fprintf(&b, "%d. `%s`\n", i+1, n)
			}
		}
		fmt.Fprintf(&b, "\n_Total: %d_", len(list))
		if len(list) > 3 {
			b.WriteString("\n_> Tap tombol Buka untuk 3 teratas; sisanya pakai `!bl del <nomor>`_")
		}

		// Tombol "Buka <n>" per entry (batas WA = 3 tombol) — tap langsung
		// mengirim `!bl del <target>`.
		mb := NewMsgBuilder().
			SetHeader(BotName, "Blacklist").
			SetBody(b.String()).
			SetFooter(channelFooter()).
			SetContextInfo(newsletterCtxInfo(ctx))
		for i := 0; i < len(list) && i < 3; i++ {
			mb.AddQRButton(fmt.Sprintf("Buka %d", i+1), fmt.Sprintf("%sbl del %s", Prefix, list[i]))
		}
		if err := mb.Send(ctx, chat); err != nil {
			// Client tidak support pesan interactive → fallback teks polos.
			sendText(ctx, chat, b.String())
		}

	default:
		sendText(ctx, chat, "❌ Sub-command tidak dikenal. Coba: `add`, `del`, `list`.")
	}
}

// blTarget — target blacklist: nomor dari args, atau grup tempat command
// dijalankan kalau args kosong. Return "" kalau tidak ada target valid.
// JID grup ("123@g.us") diterima langsung dari args (dipakai tombol Buka).
func blTarget(evt *events.Message, args string) string {
	args = strings.TrimSpace(args)
	if args != "" {
		if strings.Contains(args, "@g.us") {
			return args
		}
		return extractTargetNumber(evt, args)
	}
	if evt.Info.Chat.Server == "g.us" {
		return evt.Info.Chat.String()
	}
	return ""
}

// blLabel — tampilan target: "123@g.us" → "123", nomor → nomor.
func blLabel(target string) string {
	return strings.TrimSuffix(target, "@g.us")
}

// handleClearCache — bersihkan file sementara di temp/. File yang masih baru
// (< 5 menit) dipertahankan karena mungkin sedang dipakai proses lain
// (download/encode sedang jalan).
func handleClearCache(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	entries, err := os.ReadDir("temp")
	if err != nil {
		sendText(ctx, chat, "🧹 Folder temp kosong — tidak ada cache.")
		return
	}
	cutoff := time.Now().Add(-5 * time.Minute)
	var removed, kept int
	var freed int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			p := filepath.Join("temp", e.Name())
			if os.Remove(p) == nil {
				removed++
				freed += info.Size()
			}
		} else {
			kept++
		}
	}
	sendText(ctx, chat, fmt.Sprintf(
		"🧹 *Cache dibersihkan*\n\n"+
			"> %d file dihapus (%s)\n"+
			"> %d file aktif dipertahankan",
		removed, fmtBytes(freed), kept))
}

// fmtBytes — format ukuran byte jadi KB/MB/GB.
func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}