package main

// features_stalker_tools.go — Fitur stalker & tools.
// Berisi: wastalk (whatsmeow native, tanpa API eksternal), lookup (hackertarget.com + RDAP),
// dafont (scrape dafont.com — search + download font).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada + sendDocument baru (pola tools2.go !tofile).
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// sendDocument — upload & kirim file sebagai dokumen (pola !tofile di tools2.go).
func sendDocument(ctx context.Context, chat types.JID, data []byte, filename, mime string) error {
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("upload dokumen: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String(mime),
			FileName:      proto.String(filename),
		},
	})
	return err
}

// ─── WA Stalk ─────────────────────────────────────────────────────────────────
// Cek nomor WhatsApp terdaftar/tidak + foto profil — port dari stalker/wastalk.js
// (Ourin pakai fetchStatus; versi SC: GetUserDevices + GetProfilePictureInfo —
// keduanya fitur native whatsmeow, tanpa API pihak ketiga).
// Cmd: !wastalk <nomor>

// parsePhoneJID — normalisasi nomor user (08xx / +62xx / 62xx / 62x-xxxx) → JID.
// Mengembalikan jid kosong kalau format tidak bisa dinormalisasi.
func parsePhoneJID(s string) types.JID {
	digits := regexp.MustCompile(`[^0-9]`).ReplaceAllString(s, "")
	if digits == "" {
		return types.JID{}
	}
	if strings.HasPrefix(digits, "0") {
		digits = "62" + digits[1:]
	}
	if !strings.HasPrefix(digits, "62") {
		return types.JID{}
	}
	return types.NewJID(digits, types.DefaultUserServer)
}

func handleWAStalk(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	raw := strings.TrimSpace(args)
	var jid types.JID
	if raw != "" {
		jid = parsePhoneJID(raw)
		if jid.User == "" {
			sendText(ctx, chat, "❌ Format nomor tidak valid. Contoh: `08xxxx` atau `62xxxx`.")
			return
		}
	} else {
		// Reply/tag orangnya langsung — tanpa ketik nomor manual.
		ctxInfo := msgContextInfo(evt)
		target := ctxInfo.GetParticipant()
		if target == "" {
			if mentions := ctxInfo.GetMentionedJID(); len(mentions) > 0 {
				target = mentions[0]
			}
		}
		if target == "" {
			sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%swastalk 628123456789`, atau reply/tag orangnya.", Prefix))
			return
		}
		parsed, err := types.ParseJID(normalizeJIDString(target))
		if err != nil {
			sendText(ctx, chat, "❌ Target tidak valid.")
			return
		}
		jid, err = resolveLIDToPhone(ctx, parsed)
		if err != nil {
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
	}
	reactMsg(ctx, evt, "🔍")

	// GetUserDevices error → nomor tidak terdaftar di WhatsApp.
	devices, err := waClient.GetUserDevices(ctx, []types.JID{jid})
	if err != nil || len(devices) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("📱 *WA STALK*\n▬▬▬▬▬▬▬▬▬\n📞 *Nomor:* +%s\n❌ Tidak terdaftar di WhatsApp.", jid.User))
		return
	}

	// Dua varian: plain untuk caption media, rich untuk fallback sendText.
	plain := fmt.Sprintf("📱 WA STALK\n▬▬▬▬▬▬▬▬▬\n📞 Nomor: +%s\n✅ Terdaftar di WhatsApp (perangkat aktif: %d)", jid.User, len(devices))
	rich := fmt.Sprintf("📱 *WA STALK*\n▬▬▬▬▬▬▬▬▬\n📞 *Nomor:* +%s\n✅ Terdaftar di WhatsApp (perangkat aktif: %d)", jid.User, len(devices))

	// Foto profil opsional.
	info, err := waClient.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{})
	if err == nil && info != nil && info.URL != "" {
		if img, derr := dlGet(info.URL, nil); derr == nil {
			if serr := sendImage(ctx, chat, img, plain); serr == nil {
				reactMsg(ctx, evt, "✅")
				return
			}
		}
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, rich)
}

// ─── Lookup DNS + RDAP/WHOIS ─────────────────────────────────────────────────
// DNS lookup + info domain (RDAP/WHOIS) — port dari tools/lookup.js (Ourin).
// - DNS: api.hackertarget.com (publik, tanpa key; whois-nya butuh key → tak dipakai).
// - WHOIS: RDAP via rdap.org (IANA redirect ke registrar — publik, tanpa key).
// Cmd: !lookup <domain>

type rdapResp struct {
	Entities []struct {
		VCardArray json.RawMessage `json:"vcardArray"`
	} `json:"entities"`
	Status      []string `json:"status"`
	Events      []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
	Nameservers []struct {
		LdhName string `json:"ldhName"`
	} `json:"nameservers"`
}

// rdapSummary — ambil info domain via RDAP, ringkas jadi beberapa baris.
// Return "" kalau gagal (RDAP sebagian besar domain publik tersedia).
func rdapSummary(ctx context.Context, domain string) string {
	body, err := dlGet("https://rdap.org/domain/"+domain, nil)
	if err != nil {
		return ""
	}
	var r rdapResp
	if json.Unmarshal(body, &r) != nil {
		return ""
	}

	var sb strings.Builder
	// Registrar = entri vCard pertama yang punya tag "fn"/"org".
	// vcardArray kadang null/string → unmarshal ke [][]interface{} bisa gagal;
	// kalau gagal, lewati saja (registrar opsional).
	for _, e := range r.Entities {
		var vcs [][]interface{}
		if json.Unmarshal(e.VCardArray, &vcs) != nil {
			continue
		}
		for _, vc := range vcs {
			if len(vc) >= 4 {
				if tag, ok := vc[0].(string); ok && (tag == "fn" || tag == "org") {
					if val, ok := vc[3].(string); ok && val != "" {
						sb.WriteString("🏢 Registrar: " + val + "\n")
					}
				}
			}
		}
		if sb.Len() > 0 {
			break
		}
	}
	for _, ev := range r.Events {
		switch ev.Action {
		case "registration":
			if len(ev.Date) >= 10 {
				sb.WriteString("📅 Terdaftar: " + ev.Date[:10] + "\n")
			}
		case "expiration":
			if len(ev.Date) >= 10 {
				sb.WriteString("⏳ Kadaluarsa: " + ev.Date[:10] + "\n")
			}
		}
	}
	if len(r.Status) > 0 {
		sb.WriteString("🔒 Status: " + strings.Join(r.Status, ", ") + "\n")
	}
	var nss []string
	for _, ns := range r.Nameservers {
		nss = append(nss, strings.ToLower(ns.LdhName))
	}
	if len(nss) > 0 {
		sb.WriteString("🗂️ NS: " + strings.Join(nss, ", "))
	}
	return sb.String()
}

func handleLookup(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	domain := strings.ToLower(strings.TrimSpace(args))
	if domain == "" || strings.ContainsAny(domain, " /\\") {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%slookup example.com`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	dnsBody, err := dlGet("https://api.hackertarget.com/dnslookup/?q="+url.QueryEscape(domain), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi API.")
		return
	}
	dnsTxt := string(dnsBody)
	if strings.Contains(strings.ToLower(dnsTxt), "error") || strings.Contains(strings.ToLower(dnsTxt), "count exceeded") {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ API lookup gagal: "+dnsTxt)
		return
	}

	msg := fmt.Sprintf("🔎 *LOOKUP* — %s\n▬▬▬▬▬▬▬▬▬\n📡 *DNS:*\n%s", domain, dnsTxt)
	if rd := rdapSummary(ctx, domain); rd != "" {
		msg += "\n\n🧾 *RDAP/WHOIS:*\n" + rd
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, msg)
}

// ─── DaFont ───────────────────────────────────────────────────────────────────
// Cari & download font dari dafont.com — port dari tools/dafont.js (Ourin).
// Versi Ourin session-based (search → reply nomor); versi SC tanpa session:
// `!dafont <query>` = daftar, `!dafont <query> <nomor>` = download zip.
// Cmd: !dafont <nama font> [nomor], !font, !daffont, !carifont

var reDaFontItem = regexp.MustCompile(`<div class="lv1left dfbg">(.*?)</div>.*?<div class="dlbox"><a class="dl"[^>]*href="(//dl\.dafont\.com/dl/\?f=[^"]+)"`)

type daFontItem struct {
	Name   string
	Author string
	Link   string
}

// parseDaFont — ekstrak daftar font dari HTML search dafont.com.
func parseDaFont(htmlContent string) []daFontItem {
	rows := reDaFontItem.FindAllStringSubmatch(htmlContent, -1)
	out := make([]daFontItem, 0, len(rows))
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		raw := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(r[1], "")
		raw = html.UnescapeString(strings.ReplaceAll(raw, "&nbsp;", " "))
		raw = regexp.MustCompile(`\s+`).ReplaceAllString(raw, " ")
		name, author := raw, ""
		if i := strings.Index(raw, " by "); i >= 0 {
			name, author = raw[:i], raw[i+4:]
		}
		out = append(out, daFontItem{Name: strings.TrimSpace(name), Author: strings.TrimSpace(author), Link: "https:" + r[2]})
	}
	return out
}

func handleDaFont(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) == 0 {
		sendText(ctx, chat, fmt.Sprintf("🔤 *DaFont Search*\n\nCari font dari DaFont, lalu download dengan nomor.\n\n`Contoh: %sdafont horror`\n`Contoh: %sdafont horror 3`", Prefix, Prefix))
		return
	}

	// Nomor download = field terakhir kalau murni digit.
	idx := -1
	query := strings.Join(fields, " ")
	if n, err := strconv.Atoi(fields[len(fields)-1]); err == nil && n >= 1 && n <= 10 {
		idx = n
		query = strings.Join(fields[:len(fields)-1], " ")
	}
	if query == "" {
		sendText(ctx, chat, "❌ Masukkan kata kunci font.")
		return
	}

	reactMsg(ctx, evt, "⏳")
	body, err := dlGet("https://www.dafont.com/search.php?q="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengakses DaFont.")
		return
	}
	items := parseDaFont(string(body))
	if len(items) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Font *%s* tidak ditemukan.", query))
		return
	}

	// Mode download.
	if idx > 0 {
		if idx > len(items) {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, fmt.Sprintf("❌ Hanya ada %d hasil untuk *%s*.", len(items), query))
			return
		}
		item := items[idx-1]
		sendText(ctx, chat, fmt.Sprintf("⏳ Mengunduh *%s*...", item.Name))
		zipData, err := dlGet(item.Link, nil)
		if err != nil || len(zipData) < 100 {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal mengunduh font.")
			return
		}
		filename := "dafont_" + regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(item.Name), "_") + ".zip"
		if err := sendDocument(ctx, chat, zipData, filename, "application/zip"); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
			return
		}
		reactMsg(ctx, evt, "✅")
		return
	}

	// Mode daftar.
	max := 10
	if len(items) < max {
		max = len(items)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔤 *DaFont — %d font ditemukan*\n", len(items)))
	sb.WriteString(fmt.Sprintf("> Pencarian: *%s*\n▬▬▬▬▬▬▬▬▬\n", query))
	for i := 0; i < max; i++ {
		sb.WriteString(fmt.Sprintf("*%d.* %s\n", i+1, items[i].Name))
		if items[i].Author != "" {
			sb.WriteString(fmt.Sprintf("   └ 👤 %s\n", items[i].Author))
		}
	}
	sb.WriteString(fmt.Sprintf("\n_Download: `%sdafont %s <nomor>`_", Prefix, query))
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, sb.String())
}
