package main

// features_sports_games.go — Fitur olahraga, permainan & DNS.
// Berisi: jadwalbola (TheSportsDB, publik tanpa key), truth & dare (truthordarebot.xyz),
// dns (lookup stdlib, tanpa API).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada: dlGet, sendText, reactMsg.
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Jadwal Bola ──────────────────────────────────────────────────────────────
// Jadwal pertandingan sepak bola hari ini — port dari info/jadwalbola.js (Ourin).
// Versi Ourin pakai neoxr key (limit habis) → ganti TheSportsDB (publik, test key "3").
// Cmd: !jadwalbola [liga/tim], !bola, !football, !jadwalsepakbola

// leagueAliasID — pemetaan kata kunci bahasa Indonesia → pola nama liga
// di TheSportsDB (nama liga mereka berbahasa Inggris).
var leagueAlias = map[string]string{
	"inggris":  "english",
	"premier":  "english",
	"italia":   "serie a",
	"spanyol":  "la liga",
	"laliga":   "la liga",
	"jerman":   "bundesliga",
	"prancis":  "ligue",
	"belanda":  "eredivisie",
	"champions": "champions",
	"indonesia": "indonesian",
	"liga 1":    "indonesian",
}

var leagueEmoji = map[string]string{
	"english premier league": "🏴󠁧󠁢󠁥󠁮󠁧󠁿",
	"serie a":                "🇮🇹",
	"la liga":                "🇪🇸",
	"bundesliga":             "🇩🇪",
	"ligue 1":                "🇫🇷",
	"eredivisie":             "🇳🇱",
	"champions":              "🏆",
	"indonesian":             "🇮🇩",
}

// theSportsDBMatch — satu pertandingan dari eventsday.php.
type theSportsDBMatch struct {
	League string `json:"strLeague"`
	Home   string `json:"strHomeTeam"`
	Away   string `json:"strAwayTeam"`
	Time   string `json:"strTimestamp"` // UTC ISO8601
}

type sportsDBResp struct {
	Events []theSportsDBMatch `json:"events"`
}

// matchLeague — cari emoji liga (fallback ⚽).
func matchLeagueEmoji(league string) string {
	lower := strings.ToLower(league)
	for k, e := range leagueEmoji {
		if strings.Contains(lower, k) {
			return e
		}
	}
	return "⚽"
}

// fmtMatchTime — strTimestamp TheSportsDB (UTC, tanpa zona) → "15:04 WIB".
// Format aktual: "2026-08-15T23:00:00" (tanpa offset) — coba itu dulu,
// lalu fallback RFC3339 kalau ternyata ada zona.
func fmtMatchTime(ts string) string {
	for _, layout := range []string{"2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Add(7 * time.Hour).Format("15:04 WIB")
		}
	}
	return ts
}

// filterSportsDB — filter jadwal by kata kunci (liga/tim/liga alias).
func filterSportsDB(matches []theSportsDBMatch, leagues []string, filter string) ([]theSportsDBMatch, []string) {
	f := strings.ToLower(strings.TrimSpace(filter))
	if f == "" {
		return matches, leagues
	}
	// Terapkan alias bahasa Indonesia → nama liga TheSportsDB.
	for k, v := range leagueAlias {
		if strings.Contains(f, k) {
			f = v
			break
		}
	}
	var out []theSportsDBMatch
	var outL []string
	for i, m := range matches {
		if strings.Contains(strings.ToLower(m.Home), f) ||
			strings.Contains(strings.ToLower(m.Away), f) ||
			strings.Contains(strings.ToLower(leagues[i]), f) {
			out = append(out, m)
			outL = append(outL, leagues[i])
		}
	}
	return out, outL
}

func handleJadwalBola(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	reactMsg(ctx, evt, "⏳")

	// Tanggal hari ini (WIB) — jadwal berikutnya, bukan kemarin.
	date := time.Now().Add(7 * time.Hour).Format("2006-01-02")
	body, err := dlGet("https://www.thesportsdb.com/api/v1/json/3/eventsday.php?d="+date+"&s=Soccer", nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil jadwal bola.")
		return
	}
	var res sportsDBResp
	if json.Unmarshal(body, &res) != nil || len(res.Events) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "⛔ Tidak ada jadwal pertandingan hari ini.")
		return
	}

	matches := make([]theSportsDBMatch, 0, len(res.Events))
	leagues := make([]string, 0, len(res.Events))
	for _, e := range res.Events {
		matches = append(matches, e)
		leagues = append(leagues, e.League)
	}

	matches, leagues = filterSportsDB(matches, leagues, args)

	// Sport lain (basket dll) bisa ikut termuat — tampilkan semua yang match.
	if len(matches) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "⛔ Tidak ada jadwal cocok dengan filter itu.")
		return
	}

	const maxShow = 8
	var sb strings.Builder
	sb.WriteString("⚽ *JADWAL BOLA*\n")
	sb.WriteString("▬▬▬▬▬▬▬▬▬\n")
	for i := 0; i < len(matches) && i < maxShow; i++ {
		m := matches[i]
		sb.WriteString(fmt.Sprintf("%s *%s*\n", matchLeagueEmoji(leagues[i]), leagues[i]))
		sb.WriteString(fmt.Sprintf("🕗 %s\n", fmtMatchTime(m.Time)))
		sb.WriteString(fmt.Sprintf("⚽ %s  vs  %s\n\n", m.Home, m.Away))
	}
	if len(matches) > maxShow {
		sb.WriteString(fmt.Sprintf("…dan %d pertandingan lainnya", len(matches)-maxShow))
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, sb.String())
}

// ─── Truth & Dare ─────────────────────────────────────────────────────────────
// Random pertanyaan truth / tantangan dare — port dari fun/truth.js & dare.js
// (Ourin). Versi Ourin pakai aset JSON lokal milik SC mereka → ganti API publik
// truthordarebot.xyz (tanpa key). Rating: PG / PG13 / R (default PG).
// Cmd: !truth [rating], !dare [rating]

type truthDareResp struct {
	Question string `json:"question"`
	Type     string `json:"type"`
	Rating   string `json:"rating"`
}

// handleTruthDare — handler bersama untuk !truth dan !dare.
func handleTruthDare(ctx context.Context, evt *events.Message, args string, want string) {
	chat := evt.Info.Chat
	rating := strings.ToUpper(strings.TrimSpace(args))
	if rating == "" {
		rating = "PG"
	}
	switch rating {
	case "PG", "PG13", "R":
	default:
		sendText(ctx, chat, "❌ Rating harus `PG`, `PG13`, atau `R`.")
		return
	}

	reactMsg(ctx, evt, "🎲")
	body, err := dlGet("https://api.truthordarebot.xyz/v1/"+want+"?rating="+rating, nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil data.")
		return
	}
	var res truthDareResp
	if json.Unmarshal(body, &res) != nil || res.Question == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil data.")
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("🎯 *%s* (%s)\n\n%s", res.Type, res.Rating, res.Question))
}

func handleTruth(ctx context.Context, evt *events.Message, args string) {
	handleTruthDare(ctx, evt, args, "truth")
}

func handleDare(ctx context.Context, evt *events.Message, args string) {
	handleTruthDare(ctx, evt, args, "dare")
}

// ─── DNS Lookup ───────────────────────────────────────────────────────────────
// Lookup DNS (A/AAAA/MX/NS/TXT) — kandidat tools dari ourin.md, stdlib murni.
// Cmd: !dns <domain>

func handleDNS(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	domain := strings.ToLower(strings.TrimSpace(args))
	if domain == "" || strings.ContainsAny(domain, " /\\") {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sdns example.com`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	var sb strings.Builder
	sb.WriteString("🌐 *DNS LOOKUP* — " + domain + "\n")
	sb.WriteString("▬▬▬▬▬▬▬▬▬\n")

	// A / AAAA
	if ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", domain); err == nil && len(ips) > 0 {
		sb.WriteString("📡 *A/AAAA:*\n")
		for i, ip := range ips {
			if i >= 6 {
				break
			}
			sb.WriteString("  " + ip.String() + "\n")
		}
	} else {
		sb.WriteString("📡 *A/AAAA:* -\n")
	}

	// MX
	if mxs, err := net.DefaultResolver.LookupMX(ctx, domain); err == nil && len(mxs) > 0 {
		sb.WriteString("📧 *MX:*\n")
		for i, mx := range mxs {
			if i >= 4 {
				break
			}
			sb.WriteString(fmt.Sprintf("  %s (pref %d)\n", mx.Host, mx.Pref))
		}
	} else {
		sb.WriteString("📧 *MX:* -\n")
	}

	// NS
	if nss, err := net.DefaultResolver.LookupNS(ctx, domain); err == nil && len(nss) > 0 {
		sb.WriteString("🗂️ *NS:*\n")
		for i, ns := range nss {
			if i >= 4 {
				break
			}
			sb.WriteString("  " + ns.Host + "\n")
		}
	} else {
		sb.WriteString("🗂️ *NS:* -\n")
	}

	// TXT (ringkas, gabung semuanya yang pendek)
	if txts, err := net.DefaultResolver.LookupTXT(ctx, domain); err == nil && len(txts) > 0 {
		var parts []string
		for _, t := range txts {
			if len(t) <= 60 {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			sb.WriteString("📝 *TXT:*\n")
			for i, t := range parts {
				if i >= 4 {
					break
				}
				sb.WriteString("  \"" + t + "\"\n")
			}
		}
	}

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, sb.String())
}
