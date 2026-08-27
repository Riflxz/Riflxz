package main

// jpm.go — JPM (Jadwal Pesan Massal / broadcast) — port dari plugin Velyon
// (plugins/jpm.js) ke Go. Mode: basic, hidetag, channel, update, auto.
// Khusus owner. Settings persist di database/jpm.json; media autojpm di
// database/autojpm/.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Settings persist ─────────────────────────────────────────────────────────

const (
	jpmSettingsPath = "database/jpm.json"
	jpmMediaDir     = "database/autojpm"
)

type jpmAutoConfig struct {
	Enabled    bool   `json:"enabled"`
	IntervalMs int64  `json:"intervalMs"`
	Text       string `json:"text"`
	MediaPath  string `json:"mediaPath,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`
	LastRun    int64  `json:"lastRun"`
	NextRun    int64  `json:"nextRun"`
}

type jpmSettings struct {
	DelayMs       int64          `json:"delayMs"`
	Blacklist     []string       `json:"blacklist"`
	AutoBlacklist []string       `json:"autoBlacklist"`
	AutoJpm       *jpmAutoConfig `json:"autoJpm,omitempty"`
}

var (
	jpmSettingsOnce sync.Once
	jpmSettingsMu   sync.Mutex // serialisasi akses jpmCfg (handler vs scheduler)
	jpmCfg          jpmSettings
)

func ensureJpmSettings() {
	jpmSettingsOnce.Do(func() {
		data, err := os.ReadFile(jpmSettingsPath)
		if err != nil {
			return // belum ada file — mulai kosong
		}
		if err := json.Unmarshal(data, &jpmCfg); err != nil {
			pool.logger.Warn().Err(err).Str("path", jpmSettingsPath).Msg("jpm: JSON korup, mulai kosong")
		}
		if jpmCfg.DelayMs == 0 {
			jpmCfg.DelayMs = 5000
		}
	})
}

func saveJpmSettings() {
	jpmSettingsMu.Lock()
	defer jpmSettingsMu.Unlock()
	if err := os.MkdirAll("database", 0o755); err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: gagal buat folder database")
		return
	}
	data, err := json.MarshalIndent(jpmCfg, "", "  ")
	if err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: gagal encode settings")
		return
	}
	if err := os.WriteFile(jpmSettingsPath, data, 0o644); err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: gagal simpan settings")
	}
}

// ─── State runtime ────────────────────────────────────────────────────────────

var jpmRun = struct {
	sync.Mutex
	running bool
	stop    bool
}{}

// jpmContent — konten broadcast yang disiapkan user (dari teks/quote/media).
type jpmContent struct {
	text  string
	media *detectedMedia
	ts    time.Time
}

var jpmSessions = struct {
	sync.Mutex
	m map[string]*jpmContent
}{m: map[string]*jpmContent{}}

func jpmSetSession(sender string, c *jpmContent) {
	jpmSessions.Lock()
	jpmSessions.m[sender] = c
	jpmSessions.Unlock()
}

func jpmGetSession(sender string) *jpmContent {
	jpmSessions.Lock()
	defer jpmSessions.Unlock()
	c := jpmSessions.m[sender]
	if c == nil {
		return nil
	}
	if time.Since(c.ts) > 10*time.Minute {
		delete(jpmSessions.m, sender)
		return nil
	}
	return c
}

func jpmDelSession(sender string) {
	jpmSessions.Lock()
	delete(jpmSessions.m, sender)
	jpmSessions.Unlock()
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// jpmParseInterval "2h30m" → ms. Port parseInterval() Velyon.
func jpmParseInterval(raw string) int64 {
	if raw == "" {
		return 0
	}
	cleaned := strings.ToLower(strings.ReplaceAll(raw, " ", ""))
	re := regexp.MustCompile(`(\d+)([smhdw])`)
	matches := re.FindAllStringSubmatch(cleaned, -1)
	if len(matches) == 0 {
		return 0
	}
	combined := ""
	for _, m := range matches {
		combined += m[0]
	}
	if combined != cleaned {
		return 0
	}
	var total int64
	for _, m := range matches {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		switch m[2] {
		case "s":
			total += v * 1000
		case "m":
			total += v * 60 * 1000
		case "h":
			total += v * 3600 * 1000
		case "d":
			total += v * 86400 * 1000
		case "w":
			total += v * 7 * 86400 * 1000
		}
	}
	return total
}

func jpmFormatInterval(ms int64) string {
	if ms <= 0 {
		return "0 detik"
	}
	type unit struct {
		label string
		value int64
	}
	units := []unit{
		{"hari", 86400000},
		{"jam", 3600000},
		{"menit", 60000},
		{"detik", 1000},
	}
	var parts []string
	remaining := ms
	for _, u := range units {
		amount := remaining / u.value
		if amount > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", amount, u.label))
			remaining -= amount * u.value
		}
	}
	if len(parts) == 0 {
		return "0 detik"
	}
	return strings.Join(parts, " ")
}

func jpmPreviewText(text string) string {
	if text == "" {
		return "-"
	}
	cleaned := strings.Join(strings.Fields(text), " ")
	if len(cleaned) <= 80 {
		return cleaned
	}
	return cleaned[:77] + "..."
}

func jpmMediaLabel(med *detectedMedia) string {
	if med == nil {
		return "Tidak ada"
	}
	return med.mediaType
}

// jpmQuotedText ambil teks dari pesan yang di-quote (konversasi/extended).
func jpmQuotedText(evt *events.Message) string {
	if ci := msgContextInfo(evt); ci != nil {
		if q := ci.GetQuotedMessage(); q != nil {
			if t := q.GetConversation(); t != "" {
				return t
			}
			if e := q.GetExtendedTextMessage(); e != nil {
				return e.GetText()
			}
		}
	}
	return ""
}

// jpmCollectContent kumpulkan konten: args (teks) + media quoted (detectMedia).
func jpmCollectContent(ctx context.Context, evt *events.Message, args string) *jpmContent {
	text := strings.TrimSpace(args)
	med, err := detectMedia(ctx, evt)
	if err != nil {
		med = nil
	}
	if text == "" {
		text = jpmQuotedText(evt)
	}
	if text == "" && med != nil {
		text = med.caption
	}
	if text == "" && med == nil {
		return nil
	}
	return &jpmContent{text: text, media: med, ts: time.Now()}
}

// jpmTargetGroups daftar grup yang boleh dituju (dikurangi blacklist).
func jpmTargetGroups(ctx context.Context, auto bool) (map[string]*types.GroupInfo, int, error) {
	ensureJpmSettings()
	groups, err := waClient.GetJoinedGroups(ctx)
	if err != nil {
		return nil, 0, err
	}
	jpmSettingsMu.Lock()
	blacklist := append([]string{}, jpmCfg.Blacklist...)
	if auto {
		blacklist = append(blacklist, jpmCfg.AutoBlacklist...)
	}
	jpmSettingsMu.Unlock()

	blacklisted := 0
	out := map[string]*types.GroupInfo{}
	for _, g := range groups {
		key := g.JID.ToNonAD().String()
		if containsNumber(blacklist, key) {
			blacklisted++
			continue
		}
		out[key] = g
	}
	return out, blacklisted, nil
}

// jpmSendToGroup kirim satu konten ke satu grup (basic atau hidetag).
func jpmSendToGroup(ctx context.Context, chat types.JID, content *jpmContent, hidetag bool, mentions []string) error {
	if hidetag {
		if content.media != nil {
			return jpmSendMediaMention(ctx, chat, content.media, content.text, mentions)
		}
		return jpmSendTextMention(ctx, chat, content.text, mentions)
	}
	if content.media != nil {
		switch content.media.mediaType {
		case "image":
			return sendImage(ctx, chat, content.media.data, content.text)
		case "video":
			return sendVideo(ctx, chat, content.media.data, content.text, content.media.mime)
		case "audio":
			return sendAudio(ctx, chat, content.media.data, content.media.mime)
		default:
			return fmt.Errorf("tipe media tidak didukung: %s", content.media.mediaType)
		}
	}
	sendTextWithCtx(ctx, chat, content.text, newsletterCtxInfo(ctx))
	return nil
}

// jpmSendTextMention teks + mention (hidetag) + badge channel.
func jpmSendTextMention(ctx context.Context, chat types.JID, text string, mentions []string) error {
	ci := newsletterCtxInfo(ctx)
	if ci == nil {
		ci = &waE2E.ContextInfo{}
	}
	ci.MentionedJID = mentions
	_, err := waClient.SendMessage(ctx, chat, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: ci,
		},
	})
	return err
}

// jpmSendMediaMention media + mention (hidetag) + badge channel.
func jpmSendMediaMention(ctx context.Context, chat types.JID, med *detectedMedia, caption string, mentions []string) error {
	ci := newsletterCtxInfo(ctx)
	if ci == nil {
		ci = &waE2E.ContextInfo{}
	}
	ci.MentionedJID = mentions

	switch med.mediaType {
	case "image":
		up, err := waClient.Upload(ctx, med.data, whatsmeow.MediaImage)
		if err != nil {
			return fmt.Errorf("upload image: %w", err)
		}
		_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
				MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(uint64(len(med.data))),
				Mimetype:   proto.String("image/jpeg"), Caption: proto.String(caption),
				ContextInfo: ci,
			},
		})
		return err
	case "video":
		up, err := waClient.Upload(ctx, med.data, whatsmeow.MediaVideo)
		if err != nil {
			return fmt.Errorf("upload video: %w", err)
		}
		_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
				MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(uint64(len(med.data))),
				Mimetype:   proto.String(med.mime), Caption: proto.String(caption),
				ContextInfo: ci,
			},
		})
		return err
	case "audio":
		up, err := waClient.Upload(ctx, med.data, whatsmeow.MediaAudio)
		if err != nil {
			return fmt.Errorf("upload audio: %w", err)
		}
		_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
				MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(uint64(len(med.data))),
				Mimetype:   proto.String(med.mime), PTT: proto.Bool(false),
				ContextInfo: ci,
			},
		})
		return err
	}
	return fmt.Errorf("tipe media tidak didukung: %s", med.mediaType)
}

// ─── Dispatch ─────────────────────────────────────────────────────────────────

// handleJpm — semua command JPM (khusus owner). cmdKey = command yang diketik.
func handleJpm(ctx context.Context, evt *events.Message, args, cmdKey string) {
	if !requireOwner(ctx, evt, isOwner(senderUser(evt)) || isOwnerDB(senderUser(evt))) {
		return
	}
	switch cmdKey {
	case "stopjpm":
		handleJpmStop(ctx, evt)
	case "setdelayjpm":
		handleJpmDelay(ctx, evt, args)
	case "bljpm":
		handleJpmBlacklist(ctx, evt, args, false)
	case "blautojpm":
		handleJpmBlacklist(ctx, evt, args, true)
	case "autojpm":
		handleJpmAuto(ctx, evt, args)
	case "jpmupdate":
		handleJpmUpdate(ctx, evt, args)
	case "jpmch":
		handleJpmChannel(ctx, evt, args)
	case "jpmht":
		handleJpmDirect(ctx, evt, args)
	default: // jpm
		handleJpmMain(ctx, evt, args)
	}
}

func handleJpmStop(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	jpmRun.Lock()
	if !jpmRun.running {
		jpmRun.Unlock()
		sendText(ctx, chat, "❌ Tidak ada JPM yang sedang berjalan.")
		return
	}
	jpmRun.stop = true
	jpmRun.Unlock()
	reactMsg(ctx, evt, "⏹️")
	sendText(ctx, chat, "⏹️ *JPM Dihentikan*\n\n> Proses JPM sedang dihentikan...")
}

// ─── Main (menu interaktif) ───────────────────────────────────────────────────

func handleJpmMain(ctx context.Context, evt *events.Message, args string) {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "_") {
		handleJpmInternal(ctx, evt, trimmed)
		return
	}
	content := jpmCollectContent(ctx, evt, args)
	if content != nil {
		jpmSetSession(senderUser(evt), content)
	}
	sendJpmMenu(ctx, evt, content)
}

func sendJpmMenu(ctx context.Context, evt *events.Message, content *jpmContent) {
	chat := evt.Info.Chat
	p := Prefix
	ensureJpmSettings()

	jpmSettingsMu.Lock()
	delay := jpmCfg.DelayMs
	blCount := len(jpmCfg.Blacklist)
	autoBlCount := len(jpmCfg.AutoBlacklist)
	autoEnabled := jpmCfg.AutoJpm != nil && jpmCfg.AutoJpm.Enabled
	jpmSettingsMu.Unlock()
	jpmRun.Lock()
	running := jpmRun.running
	jpmRun.Unlock()

	status := "❌ Nonaktif"
	if autoEnabled {
		status = "✅ Aktif"
	}
	runningLabel := "Tidak"
	if running {
		runningLabel = "⚠️ Ya"
	}

	body := fmt.Sprintf("📢 *JPM — Sistem Broadcast Massal*\n\n"+
		"Kirim pesan ke seluruh grup, channel, atau target tertentu secara otomatis maupun manual.\n\n"+
		"*Status saat ini:*\n"+
		"> ⏱️ Delay: *%.1f detik*\n"+
		"> 🔄 AutoJPM: *%s*\n"+
		"> 🚫 Blacklist JPM: *%d grup*\n"+
		"> 🚫 Blacklist Auto: *%d grup*\n"+
		"> 📢 JPM Berjalan: *%s*",
		float64(delay)/1000, status, blCount, autoBlCount, runningLabel)

	if content != nil {
		body += fmt.Sprintf("\n\n📝 *Konten yang siap dikirim:*\n"+
			"> Teks: *%s*\n"+
			"> Media: *%s*\n\n"+
			"_Pilih mode pengiriman di bawah untuk mulai broadcast_",
			jpmPreviewText(content.text), jpmMediaLabel(content.media))
	} else {
		body += fmt.Sprintf("\n\n💡 *Cara pakai:*\n"+
			"1. Kirim teks, foto, audio, atau video\n"+
			"2. Reply pesan tersebut dengan *%sjpm*\n"+
			"3. Pilih mode pengiriman dari tombol di bawah\n\n"+
			"_Atau langsung pilih mode dulu, lalu kirim kontennya_", p)
	}

	sections := []listSection{
		{title: "📨 MODE BROADCAST", rows: []listRow{
			{title: "📢 JPM Basic", desc: "Kirim pesan ke semua grup tanpa tag", id: p + "jpm _mode_basic"},
			{title: "👁️ JPM Hidetag", desc: "Kirim pesan ke semua grup, tag tersembunyi", id: p + "jpm _mode_hidetag"},
			{title: "📺 JPM Channel", desc: "Kirim pesan ke semua channel newsletter", id: p + "jpm _mode_channel"},
			{title: "🚀 JPM Update", desc: "Broadcast changelog/update ke semua grup", id: p + "jpm _mode_update"},
			{title: "🔄 Auto JPM", desc: "Atur jadwal siaran otomatis berdasar interval", id: p + "jpm _mode_autojpm"},
		}},
		{title: "⚙️ PENGATURAN", rows: []listRow{
			{title: "⏱️ Atur Delay JPM", desc: fmt.Sprintf("Delay saat ini: %.1fs", float64(delay)/1000), id: p + "jpm _set_delay"},
			{title: "🚫 Blacklist JPM", desc: fmt.Sprintf("Kelola grup yang dikecualikan dari JPM (%d)", blCount), id: p + "jpm _bl_jpm"},
			{title: "🚫 Blacklist AutoJPM", desc: fmt.Sprintf("Kelola grup yang dikecualikan dari AutoJPM (%d)", autoBlCount), id: p + "jpm _bl_autojpm"},
			{title: "⏹️ Stop JPM", desc: "Hentikan JPM yang sedang berjalan", id: p + "jpm _stop"},
			{title: "📊 Status AutoJPM", desc: "Cek jadwal & detail auto JPM", id: p + "jpm _autojpm_status"},
		}},
	}

	b := NewMsgBuilder().
		SetHeader(BotName+" JPM", fmt.Sprintf("by %s • prefix: %s", BotDeveloper, p)).
		SetBody(body).
		SetFooter(BotName + " JPM System").
		SetContextInfo(newsletterCtxInfo(ctx)).
		AddSelectButton("📢 Pilih Mode JPM", sections)
	if err := b.Send(ctx, chat); err == nil {
		return
	}

	// Fallback: ListMessage → plain text.
	var lmSections []*waE2E.ListMessage_Section
	for _, sec := range sections {
		var rows []*waE2E.ListMessage_Row
		for _, r := range sec.rows {
			rows = append(rows, &waE2E.ListMessage_Row{
				RowID: proto.String(r.id), Title: proto.String(r.title), Description: proto.String(r.desc),
			})
		}
		lmSections = append(lmSections, &waE2E.ListMessage_Section{Title: proto.String(sec.title), Rows: rows})
	}
	_, err := waClient.SendMessage(ctx, chat, &waE2E.Message{
		ListMessage: &waE2E.ListMessage{
			Title:       proto.String(BotName + " JPM"),
			Description: proto.String(body),
			ButtonText:  proto.String("📢 Pilih Mode JPM"),
			FooterText:  proto.String(BotName + " JPM System"),
			ListType:    waE2E.ListMessage_SINGLE_SELECT.Enum(),
			Sections:    lmSections,
		},
	})
	if err == nil {
		return
	}
	sendText(ctx, chat, body)
}

// handleJpmInternal — command internal dari tombol menu ("_mode_..." dll).
func handleJpmInternal(ctx context.Context, evt *events.Message, cmd string) {
	switch cmd {
	case "_mode_basic":
		jpmExecuteWithSession(ctx, evt, "basic")
	case "_mode_hidetag":
		jpmExecuteWithSession(ctx, evt, "hidetag")
	case "_mode_channel":
		jpmExecuteWithSession(ctx, evt, "channel")
	case "_mode_update":
		jpmExecuteWithSession(ctx, evt, "update")
	case "_mode_autojpm":
		jpmAutoIntervalMenu(ctx, evt)
	case "_set_delay":
		handleJpmDelay(ctx, evt, "")
	case "_bl_jpm":
		handleJpmBlacklist(ctx, evt, "", false)
	case "_bl_autojpm":
		handleJpmBlacklist(ctx, evt, "", true)
	case "_autojpm_status":
		jpmAutoStatus(ctx, evt)
	case "_stop":
		handleJpmStop(ctx, evt)
	case "_help":
		jpmHelp(ctx, evt)
	default:
		if strings.HasPrefix(cmd, "_autojpm_interval_") {
			jpmCompleteAutoSetup(ctx, evt, strings.TrimPrefix(cmd, "_autojpm_interval_"))
			return
		}
		if strings.HasPrefix(cmd, "_delay_") {
			ms, err := strconv.ParseInt(strings.TrimPrefix(cmd, "_delay_"), 10, 64)
			if err == nil && ms >= 1000 && ms <= 30000 {
				handleJpmDelay(ctx, evt, strconv.FormatInt(ms, 10))
				return
			}
		}
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Perintah tidak dikenali. Ketik *%sjpm* untuk membuka menu.", Prefix))
	}
}

// ─── Broadcast ────────────────────────────────────────────────────────────────

func jpmExecuteWithSession(ctx context.Context, evt *events.Message, mode string) {
	chat := evt.Info.Chat
	content := jpmGetSession(senderUser(evt))
	if content == nil {
		sendText(ctx, chat, "❌ *Tidak Ada Konten*\n\n"+
			"Kirim pesan, foto, audio, atau video terlebih dahulu, lalu reply dengan *"+Prefix+"jpm* dan pilih mode pengiriman.\n\n"+
			"*Cara yang benar:*\n"+
			"1. Kirim teks/foto/video/audio\n"+
			"2. Reply pesan tersebut dengan *"+Prefix+"jpm*\n"+
			"3. Pilih mode dari tombol yang muncul")
		return
	}
	if mode == "update" {
		jpmUpdateWithContent(ctx, evt, content.text)
		return
	}
	if mode == "channel" {
		jpmChannelWithContent(ctx, evt, content)
		return
	}

	jpmRun.Lock()
	if jpmRun.running {
		jpmRun.Unlock()
		sendText(ctx, chat, "❌ *JPM Sedang Berjalan*\n\n> Ketik *"+Prefix+"stopjpm* untuk menghentikan terlebih dahulu.")
		return
	}
	jpmRun.Unlock()

	reactMsg(ctx, evt, "📢")
	groups, blacklisted, err := jpmTargetGroups(ctx, false)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar grup: "+err.Error())
		return
	}
	if len(groups) == 0 {
		reactMsg(ctx, evt, "❌")
		msg := "❌ *Tidak Ada Grup*\n\n> Bot tidak menemukan grup yang bisa dituju"
		if blacklisted > 0 {
			msg += fmt.Sprintf(" (%d grup di-blacklist)", blacklisted)
		}
		sendText(ctx, chat, msg)
		return
	}
	jpmRunBroadcast(ctx, evt, mode, content, groups)
	jpmDelSession(senderUser(evt))
}

// jpmRunBroadcast kirim konten ke semua grup dengan jeda & dukungan stop.
func jpmRunBroadcast(ctx context.Context, evt *events.Message, mode string, content *jpmContent, groups map[string]*types.GroupInfo) {
	chat := evt.Info.Chat
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	delay := jpmCfg.DelayMs
	jpmSettingsMu.Unlock()

	isHidetag := mode == "hidetag"
	modeLabel := map[string]string{"hidetag": "Hidetag", "channel": "Channel", "update": "Update"}[mode]
	if modeLabel == "" {
		modeLabel = "Basic"
	}

	sendText(ctx, chat, fmt.Sprintf("📢 *JPM %s Dimulai*\n\n"+
		"> 📝 Pesan: *%s*\n"+
		"> 📷 Media: *%s*\n"+
		"> 👥 Target: *%d* grup\n"+
		"> ⏱️ Jeda: *%.1f detik*\n"+
		"> 📊 Estimasi: *%d menit*\n\n"+
		"_Sedang mengirim ke semua target..._",
		modeLabel, jpmPreviewText(content.text), jpmMediaLabel(content.media), len(groups),
		float64(delay)/1000, int64(len(groups))*delay/60000))

	jpmRun.Lock()
	jpmRun.running = true
	jpmRun.stop = false
	jpmRun.Unlock()

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	success, failed := 0, 0
	for _, key := range keys {
		jpmRun.Lock()
		stop := jpmRun.stop
		jpmRun.Unlock()
		if stop {
			jpmRun.Lock()
			jpmRun.running = false
			jpmRun.stop = false
			jpmRun.Unlock()
			sendText(ctx, chat, fmt.Sprintf("⏹️ *JPM Dihentikan*\n\n"+
				"> ✅ Berhasil: *%d*\n"+
				"> ❌ Gagal: *%d*\n"+
				"> ⏸️ Sisa: *%d*",
				success, failed, len(keys)-success-failed))
			return
		}

		g := groups[key]
		var mentions []string
		if isHidetag {
			for _, p := range g.Participants {
				mentions = append(mentions, p.JID.ToNonAD().String())
			}
		}
		if err := jpmSendToGroup(ctx, g.JID, content, isHidetag, mentions); err != nil {
			failed++
		} else {
			success++
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	jpmRun.Lock()
	jpmRun.running = false
	jpmRun.Unlock()
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ *JPM %s Selesai!*\n\n"+
		"> ✅ Berhasil: *%d*\n"+
		"> ❌ Gagal: *%d*\n"+
		"> 📊 Total: *%d*",
		modeLabel, success, failed, len(keys)))
}

// ─── JPM Hidetag (direct) ─────────────────────────────────────────────────────

func handleJpmDirect(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	content := jpmCollectContent(ctx, evt, args)
	if content == nil {
		sendText(ctx, chat, "📢 *JPM Hidetag*\n\n"+
			"Kirim pesan broadcast ke seluruh grup dengan tag semua member secara tersembunyi.\n\n"+
			"*PENGGUNAAN:*\n"+
			"> *"+Prefix+"jpmht <pesan>*\n"+
			"> *"+Prefix+"jpmht* (reply foto/video)\n\n"+
			"*CONTOH:*\n"+
			"> *"+Prefix+"jpmht Halo semuanya! Jangan lupa event besok.*")
		return
	}

	jpmRun.Lock()
	if jpmRun.running {
		jpmRun.Unlock()
		sendText(ctx, chat, "❌ *JPM Sedang Berjalan*\n\n> Ketik *"+Prefix+"stopjpm* untuk menghentikan.")
		return
	}
	jpmRun.Unlock()

	reactMsg(ctx, evt, "📢")
	groups, blacklisted, err := jpmTargetGroups(ctx, false)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar grup: "+err.Error())
		return
	}
	if len(groups) == 0 {
		reactMsg(ctx, evt, "❌")
		msg := "❌ *Tidak Ada Grup*\n\n> Bot tidak menemukan grup yang bisa dituju"
		if blacklisted > 0 {
			msg += fmt.Sprintf(" (%d grup di-blacklist)", blacklisted)
		}
		sendText(ctx, chat, msg)
		return
	}
	jpmRunBroadcast(ctx, evt, "hidetag", content, groups)
}

// ─── JPM Channel ──────────────────────────────────────────────────────────────

func handleJpmChannel(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	content := jpmCollectContent(ctx, evt, args)
	if content == nil {
		sendText(ctx, chat, "📺 *JPM Channel*\n\n"+
			"Kirim pesan ke semua channel WhatsApp yang di-subscribe bot.\n\n"+
			"*PENGGUNAAN:*\n"+
			"> *"+Prefix+"jpmch <pesan>*\n"+
			"> *"+Prefix+"jpmch* (reply foto/video)\n\n"+
			"*CONTOH:*\n"+
			"> *"+Prefix+"jpmch Halo semua, ikuti update terbaru kami!*")
		return
	}
	jpmChannelWithContent(ctx, evt, content)
}

func jpmChannelWithContent(ctx context.Context, evt *events.Message, content *jpmContent) {
	chat := evt.Info.Chat

	jpmRun.Lock()
	if jpmRun.running {
		jpmRun.Unlock()
		sendText(ctx, chat, "❌ *JPM Sedang Berjalan*\n\n> Ketik *"+Prefix+"stopjpm* untuk menghentikan.")
		return
	}
	jpmRun.Unlock()

	reactMsg(ctx, evt, "📢")
	channels, err := fetchChannelList(ctx)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar channel: "+err.Error())
		return
	}
	if len(channels) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ *Tidak Ada Channel*\n\n> Bot belum subscribe channel apapun")
		return
	}

	ensureJpmSettings()
	jpmSettingsMu.Lock()
	delay := jpmCfg.DelayMs
	jpmSettingsMu.Unlock()

	sendText(ctx, chat, fmt.Sprintf("📢 *JPM Channel Dimulai*\n\n"+
		"> 📝 Pesan: *%s*\n"+
		"> 📷 Media: *%s*\n"+
		"> 📺 Target: *%d* channel\n"+
		"> ⏱️ Jeda: *%.1f detik*\n\n"+
		"_Sedang mengirim ke semua channel..._",
		jpmPreviewText(content.text), jpmMediaLabel(content.media), len(channels), float64(delay)/1000))

	jpmRun.Lock()
	jpmRun.running = true
	jpmRun.stop = false
	jpmRun.Unlock()

	success, failed := 0, 0
	for _, ch := range channels {
		jpmRun.Lock()
		stop := jpmRun.stop
		jpmRun.Unlock()
		if stop {
			jpmRun.Lock()
			jpmRun.running = false
			jpmRun.stop = false
			jpmRun.Unlock()
			sendText(ctx, chat, fmt.Sprintf("⏹️ *JPM Channel Dihentikan*\n\n"+
				"> ✅ Berhasil: *%d*\n"+
				"> ❌ Gagal: *%d*",
				success, failed))
			return
		}
		var err error
		if content.media != nil {
			err = sendMediaToNewsletter(ctx, ch.JID, content.media)
		} else {
			_, err = waClient.SendMessage(ctx, ch.JID, &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text:        proto.String(content.text),
					ContextInfo: newsletterCtxInfo(ctx),
				},
			})
		}
		if err != nil {
			failed++
		} else {
			success++
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	jpmRun.Lock()
	jpmRun.running = false
	jpmRun.Unlock()
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ *JPM Channel Selesai!*\n\n"+
		"> ✅ Berhasil: *%d*\n"+
		"> ❌ Gagal: *%d*\n"+
		"> 📊 Total: *%d*",
		success, failed, len(channels)))
}

// ─── JPM Update ───────────────────────────────────────────────────────────────

func handleJpmUpdate(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		text = jpmQuotedText(evt)
	}
	if text == "" {
		sendText(ctx, chat, "🚀 *JPM Update*\n\n"+
			"Kirim informasi update / changelog ke seluruh grup!\n\n"+
			"*FORMAT:*\n"+
			"> *"+Prefix+"jpmupdate <versi> | <isi changelog>*\n\n"+
			"*CONTOH:*\n"+
			"> *"+Prefix+"jpmupdate v3.0 | Fitur Baru: - JPM Hidetag - Sistem AFK*")
		return
	}
	jpmUpdateWithContent(ctx, evt, text)
}

func jpmUpdateWithContent(ctx context.Context, evt *events.Message, input string) {
	chat := evt.Info.Chat

	jpmRun.Lock()
	if jpmRun.running {
		jpmRun.Unlock()
		sendText(ctx, chat, "❌ *JPM Sedang Berjalan*\n\n> Ketik *"+Prefix+"stopjpm* untuk menghentikan.")
		return
	}
	jpmRun.Unlock()

	version := "v1.0"
	changelog := input
	if strings.Contains(input, "|") {
		parts := strings.SplitN(input, "|", 2)
		version = strings.TrimSpace(parts[0])
		changelog = strings.TrimSpace(parts[1])
	}
	if changelog == "" {
		sendText(ctx, chat, "❌ Changelog tidak boleh kosong!")
		return
	}

	reactMsg(ctx, evt, "🕕")
	groups, blacklisted, err := jpmTargetGroups(ctx, false)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil daftar grup: "+err.Error())
		return
	}
	if len(groups) == 0 {
		reactMsg(ctx, evt, "❌")
		msg := "❌ *Tidak Ada Grup*\n\n> Bot tidak menemukan grup yang bisa dituju"
		if blacklisted > 0 {
			msg += fmt.Sprintf(" (%d grup di-blacklist)", blacklisted)
		}
		sendText(ctx, chat, msg)
		return
	}

	dateStr := time.Now().Format("02 January 2006")
	updateMsg := fmt.Sprintf("🚀 *UPDATE !! | %s*\n\n"+
		"📅 *Tanggal:* %s\n\n"+
		"*CHANGELOG:*\n%s\n\n"+
		"*CATATAN TERBARU:*\n"+
		"> 💡 Ketik *%smenu* untuk mengeksplorasi fitur-fitur ini.\n"+
		"> 📢 _Terima kasih telah menggunakan %s_",
		version, dateStr, changelog, Prefix, BotName)

	ensureJpmSettings()
	jpmSettingsMu.Lock()
	delay := jpmCfg.DelayMs
	jpmSettingsMu.Unlock()

	sendText(ctx, chat, fmt.Sprintf("📢 *JPM Update Dimulai*\n\n"+
		"> 🏷️ Versi: *%s*\n"+
		"> 👥 Target: *%d* grup\n"+
		"> ⏱️ Jeda: *%.1f detik*\n\n"+
		"_Sedang broadcast update ke semua grup..._",
		version, len(groups), float64(delay)/1000))

	jpmRun.Lock()
	jpmRun.running = true
	jpmRun.stop = false
	jpmRun.Unlock()

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	success, failed := 0, 0
	for _, key := range keys {
		jpmRun.Lock()
		stop := jpmRun.stop
		jpmRun.Unlock()
		if stop {
			jpmRun.Lock()
			jpmRun.running = false
			jpmRun.stop = false
			jpmRun.Unlock()
			sendText(ctx, chat, fmt.Sprintf("⏹️ *JPM Update Dihentikan*\n\n"+
				"> ✅ Berhasil: *%d*\n"+
				"> ❌ Gagal: *%d*\n"+
				"> ⏸️ Sisa: *%d*",
				success, failed, len(keys)-success-failed))
			return
		}
		if err := jpmSendToGroup(ctx, groups[key].JID, &jpmContent{text: updateMsg}, false, nil); err != nil {
			failed++
		} else {
			success++
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	jpmRun.Lock()
	jpmRun.running = false
	jpmRun.Unlock()
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ *JPM Update Selesai!*\n\n"+
		"> ✅ Sukses: *%d*\n"+
		"> ❌ Gagal: *%d*\n"+
		"> 📊 Total: *%d*",
		success, failed, len(keys)))
}

// ─── Auto JPM ─────────────────────────────────────────────────────────────────

func handleJpmAuto(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ensureJpmSettings()
	input := strings.TrimSpace(args)
	if input == "" {
		jpmAutoIntervalMenu(ctx, evt)
		return
	}
	parts := strings.Fields(input)
	action := strings.ToLower(parts[0])

	switch action {
	case "off", "stop", "disable":
		jpmSettingsMu.Lock()
		enabled := jpmCfg.AutoJpm != nil && jpmCfg.AutoJpm.Enabled
		if jpmCfg.AutoJpm != nil {
			jpmCfg.AutoJpm.Enabled = false
		}
		jpmSettingsMu.Unlock()
		if !enabled {
			sendText(ctx, chat, "ℹ️ AutoJPM sudah nonaktif.")
			return
		}
		saveJpmSettings()
		stopAutoJpmScheduler()
		sendText(ctx, chat, "✅ *AutoJPM Dinonaktifkan*\n\n> Jadwal siaran otomatis telah dimatikan.")
		return
	case "status", "info":
		jpmAutoStatus(ctx, evt)
		return
	case "on", "start", "enable":
	default:
		sendText(ctx, chat, fmt.Sprintf("❌ Format salah. Gunakan *%sautojpm on/off/status*.", Prefix))
		return
	}

	if len(parts) < 2 {
		jpmAutoIntervalMenu(ctx, evt)
		return
	}
	intervalMs := jpmParseInterval(parts[1])
	if intervalMs == 0 {
		sendText(ctx, chat, "❌ Interval tidak valid. Contoh: *10m*, *1h*, *2h30m*, *1d*.")
		return
	}
	if intervalMs < 15*60*1000 {
		sendText(ctx, chat, "❌ Interval minimal *15 menit* untuk mencegah spam.")
		return
	}

	messageText := strings.TrimSpace(strings.TrimPrefix(input, parts[0]+" "+parts[1]))
	messageText = strings.ReplaceAll(messageText, `\n`, "\n")
	content := jpmCollectContent(ctx, evt, messageText)

	jpmSettingsMu.Lock()
	hasExisting := jpmCfg.AutoJpm != nil && (jpmCfg.AutoJpm.Text != "" || jpmCfg.AutoJpm.MediaPath != "")
	jpmSettingsMu.Unlock()
	if content == nil && !hasExisting {
		sendText(ctx, chat, "❌ Pesan atau media wajib diisi.")
		return
	}

	jpmSaveAutoConfig(ctx, evt, intervalMs, content)
	jpmSettingsMu.Lock()
	cfg := jpmCfg.AutoJpm
	jpmSettingsMu.Unlock()
	sendText(ctx, chat, fmt.Sprintf("✅ *Auto JPM Aktif!*\n\n"+
		"> ⏱️ Interval: *%s*\n"+
		"> 🕒 Pertama kali: *%s*\n"+
		"> 📷 Media: *%s*\n"+
		"> 📝 Pesan: *%s*",
		jpmFormatInterval(intervalMs),
		time.UnixMilli(cfg.NextRun).Format("02 Jan 2006 15:04"),
		jpmAutoMediaLabel(), jpmPreviewText(cfg.Text)))
}

// jpmAutoIntervalMenu — menu pilih interval (single_select).
func jpmAutoIntervalMenu(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	p := Prefix
	content := jpmGetSession(senderUser(evt))

	body := "🔄 *Auto JPM — Sesi Pengaturan*\n\n" +
		"Bot akan mengirim pesan secara otomatis ke seluruh grup berdasarkan interval waktu yang kamu tentukan.\n\n"
	if content != nil {
		body += fmt.Sprintf("📝 *Konten yang akan dikirim:*\n"+
			"> Teks: *%s*\n"+
			"> Media: *%s*\n\n",
			jpmPreviewText(content.text), jpmMediaLabel(content.media))
	}
	body += "*Pilih interval di bawah:*\n" +
		"> Semakin lama interval, semakin aman dari spam detection.\n" +
		"> Minimal: *15 menit*"

	intervals := []struct{ label, desc, id string }{
		{"🕐 15 Menit", "Cocok untuk pengingat singkat", "_autojpm_interval_15m"},
		{"🕐 30 Menit", "Interval standar", "_autojpm_interval_30m"},
		{"🕐 1 Jam", "Paling umum digunakan", "_autojpm_interval_1h"},
		{"🕐 2 Jam", "Aman & tidak mengganggu", "_autojpm_interval_2h"},
		{"🕐 3 Jam", "Sangat aman dari spam", "_autojpm_interval_3h"},
		{"🕐 6 Jam", "Setengah hari sekali", "_autojpm_interval_6h"},
		{"🕐 12 Jam", "Dua kali sehari", "_autojpm_interval_12h"},
		{"🕐 1 Hari", "Sekali sehari", "_autojpm_interval_1d"},
	}
	var rows []listRow
	for _, iv := range intervals {
		rows = append(rows, listRow{title: iv.label, desc: iv.desc, id: p + "jpm " + iv.id})
	}
	sections := []listSection{
		{title: "⏱️ INTERVAL POPULER", rows: rows},
		{title: "⚙️ KUSTOM", rows: []listRow{
			{title: "✏️ Input Manual", desc: "Ketik interval sendiri (contoh: !autojpm on 2h30m pesan)", id: p + "jpm _help"},
		}},
	}

	b := NewMsgBuilder().
		SetHeader(BotName+" AutoJPM", fmt.Sprintf("by %s • prefix: %s", BotDeveloper, p)).
		SetBody(body).
		SetFooter(BotName + " AutoJPM").
		SetContextInfo(newsletterCtxInfo(ctx)).
		AddSelectButton("⏱️ Pilih Interval", sections)
	if err := b.Send(ctx, chat); err == nil {
		return
	}

	var lmSections []*waE2E.ListMessage_Section
	for _, sec := range sections {
		var rows []*waE2E.ListMessage_Row
		for _, r := range sec.rows {
			rows = append(rows, &waE2E.ListMessage_Row{
				RowID: proto.String(r.id), Title: proto.String(r.title), Description: proto.String(r.desc),
			})
		}
		lmSections = append(lmSections, &waE2E.ListMessage_Section{Title: proto.String(sec.title), Rows: rows})
	}
	_, err := waClient.SendMessage(ctx, chat, &waE2E.Message{
		ListMessage: &waE2E.ListMessage{
			Title: proto.String(BotName + " AutoJPM"), Description: proto.String(body),
			ButtonText: proto.String("⏱️ Pilih Interval"), FooterText: proto.String(BotName + " AutoJPM"),
			ListType: waE2E.ListMessage_SINGLE_SELECT.Enum(), Sections: lmSections,
		},
	})
	if err == nil {
		return
	}
	sendText(ctx, chat, body)
}

// jpmCompleteAutoSetup — selesaikan setup autojpm dari tombol interval.
func jpmCompleteAutoSetup(ctx context.Context, evt *events.Message, intervalStr string) {
	chat := evt.Info.Chat
	intervalMs := jpmParseInterval(intervalStr)
	if intervalMs == 0 {
		sendText(ctx, chat, "❌ Interval tidak valid. Contoh: *15m*, *1h*, *2h30m*, *1d*")
		return
	}
	if intervalMs < 15*60*1000 {
		sendText(ctx, chat, "❌ Interval minimal *15 menit* untuk mencegah spam.")
		return
	}

	content := jpmGetSession(senderUser(evt))
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	hasExisting := jpmCfg.AutoJpm != nil && (jpmCfg.AutoJpm.Text != "" || jpmCfg.AutoJpm.MediaPath != "")
	jpmSettingsMu.Unlock()
	if content == nil && !hasExisting {
		sendText(ctx, chat, "❌ *Pesan atau Media Wajib Diisi*\n\n"+
			"> Kirim konten terlebih dahulu, lalu ketik *"+Prefix+"jpm* dan pilih Auto JPM.")
		return
	}

	jpmSaveAutoConfig(ctx, evt, intervalMs, content)
	jpmDelSession(senderUser(evt))
	jpmSettingsMu.Lock()
	cfg := jpmCfg.AutoJpm
	jpmSettingsMu.Unlock()
	sendText(ctx, chat, fmt.Sprintf("✅ *Auto JPM Aktif!*\n\n"+
		"> ⏱️ Interval: *%s*\n"+
		"> 🕒 Pertama kali: *%s*\n"+
		"> 📷 Media: *%s*\n"+
		"> 📝 Pesan: *%s*\n\n"+
		"_AutoJPM akan berjalan secara otomatis sesuai jadwal._",
		jpmFormatInterval(intervalMs),
		time.UnixMilli(cfg.NextRun).Format("02 Jan 2006 15:04"),
		jpmAutoMediaLabel(), jpmPreviewText(cfg.Text)))
}

// jpmSaveAutoConfig simpan config autojpm (pesan/media/interval) + start scheduler.
func jpmSaveAutoConfig(ctx context.Context, evt *events.Message, intervalMs int64, content *jpmContent) {
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	cfg := &jpmAutoConfig{}
	if jpmCfg.AutoJpm != nil {
		cfg = jpmCfg.AutoJpm
	}
	if content != nil {
		cfg.Text = content.text
		if content.media != nil {
			if path := jpmSaveMediaFile(content.media); path != "" {
				cfg.MediaPath = path
				cfg.MediaType = content.media.mediaType
			}
		}
	}
	cfg.Enabled = true
	cfg.IntervalMs = intervalMs
	cfg.LastRun = 0
	cfg.NextRun = time.Now().Add(time.Duration(intervalMs) * time.Millisecond).UnixMilli()
	jpmCfg.AutoJpm = cfg
	jpmSettingsMu.Unlock()
	saveJpmSettings()
	startAutoJpmScheduler()
}

func jpmSaveMediaFile(med *detectedMedia) string {
	if err := os.MkdirAll(jpmMediaDir, 0o755); err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: gagal buat folder media autojpm")
		return ""
	}
	ext := "bin"
	switch med.mediaType {
	case "image":
		ext = "jpg"
	case "video":
		ext = "mp4"
	case "audio":
		ext = "mp3"
	}
	path := filepath.Join(jpmMediaDir, fmt.Sprintf("autojpm_%d.%s", time.Now().UnixMilli(), ext))
	if err := os.WriteFile(path, med.data, 0o644); err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: gagal simpan media autojpm")
		return ""
	}
	return path
}

func jpmAutoStatus(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	cfg := jpmCfg.AutoJpm
	jpmSettingsMu.Unlock()
	if cfg == nil {
		sendText(ctx, chat, "ℹ️ AutoJPM belum dikonfigurasi. Ketik *"+Prefix+"jpm* untuk mengatur.")
		return
	}
	status := "❌ Nonaktif"
	if cfg.Enabled {
		status = "✅ Aktif"
	}
	lastRun := "Belum pernah"
	if cfg.LastRun > 0 {
		lastRun = time.UnixMilli(cfg.LastRun).Format("02 Jan 2006 15:04")
	}
	nextRun := "Belum dijadwalkan"
	if cfg.NextRun > 0 {
		nextRun = time.UnixMilli(cfg.NextRun).Format("02 Jan 2006 15:04")
	}
	media := "Tidak ada"
	if cfg.MediaPath != "" {
		media = strings.ToUpper(cfg.MediaType)
	}
	sendText(ctx, chat, fmt.Sprintf("📢 *Status Auto JPM*\n\n"+
		"> Status: *%s*\n"+
		"> Interval: *%s*\n\n"+
		"*Jadwal:*\n"+
		"> Terakhir: *%s*\n"+
		"> Berikutnya: *%s*\n\n"+
		"*Pesan:*\n"+
		"> Teks: *%s*\n"+
		"> Media: *%s*",
		status, jpmFormatInterval(cfg.IntervalMs), lastRun, nextRun,
		jpmPreviewText(cfg.Text), media))
}

func jpmAutoMediaLabel() string {
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	defer jpmSettingsMu.Unlock()
	if jpmCfg.AutoJpm == nil || jpmCfg.AutoJpm.MediaPath == "" {
		return "Tidak ada"
	}
	return strings.ToUpper(jpmCfg.AutoJpm.MediaType)
}

// ─── Scheduler ────────────────────────────────────────────────────────────────

var (
	jpmSchedMu      sync.Mutex
	jpmSchedStopCh  chan struct{}
	jpmSchedRunning bool
)

func startAutoJpmScheduler() {
	jpmSchedMu.Lock()
	defer jpmSchedMu.Unlock()
	if jpmSchedRunning {
		return
	}
	jpmSchedStopCh = make(chan struct{})
	jpmSchedRunning = true
	go jpmAutoLoop(jpmSchedStopCh)
}

func stopAutoJpmScheduler() {
	jpmSchedMu.Lock()
	defer jpmSchedMu.Unlock()
	if !jpmSchedRunning {
		return
	}
	close(jpmSchedStopCh)
	jpmSchedRunning = false
}

// startAutoJpmSchedulerIfEnabled — dipanggil saat bot connect: kalau config
// autojpm masih enabled (persist), lanjutkan jadwal setelah restart.
func startAutoJpmSchedulerIfEnabled() {
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	enabled := jpmCfg.AutoJpm != nil && jpmCfg.AutoJpm.Enabled
	jpmSettingsMu.Unlock()
	if enabled {
		startAutoJpmScheduler()
		pool.logger.Info().Msg("jpm: autojpm scheduler dilanjutkan dari config")
	}
}

func jpmAutoLoop(stopCh chan struct{}) {
	for {
		ensureJpmSettings()
		jpmSettingsMu.Lock()
		cfg := jpmCfg.AutoJpm
		if cfg == nil || !cfg.Enabled {
			jpmSettingsMu.Unlock()
			jpmSchedMu.Lock()
			jpmSchedRunning = false
			jpmSchedMu.Unlock()
			return
		}
		wait := time.Until(time.UnixMilli(cfg.NextRun))
		jpmSettingsMu.Unlock()
		if wait < 0 {
			wait = 0
		}
		select {
		case <-time.After(wait):
		case <-stopCh:
			jpmSchedMu.Lock()
			jpmSchedRunning = false
			jpmSchedMu.Unlock()
			return
		}
		jpmRunAutoBroadcast()
	}
}

// jpmRunAutoBroadcast — kirim konten autojpm ke semua grup (pakai blacklist auto).
func jpmRunAutoBroadcast() {
	ctx := appCtx
	ensureJpmSettings()
	jpmSettingsMu.Lock()
	cfg := jpmCfg.AutoJpm
	if cfg == nil || !cfg.Enabled {
		jpmSettingsMu.Unlock()
		return
	}
	text := cfg.Text
	mediaPath := cfg.MediaPath
	mediaType := cfg.MediaType
	intervalMs := cfg.IntervalMs
	delay := jpmCfg.DelayMs
	jpmSettingsMu.Unlock()

	var med *detectedMedia
	if mediaPath != "" {
		if data, err := os.ReadFile(mediaPath); err == nil {
			med = &detectedMedia{mediaType: mediaType, data: data}
		} else {
			pool.logger.Warn().Err(err).Str("path", mediaPath).Msg("jpm: media autojpm tidak terbaca")
		}
	}
	content := &jpmContent{text: text, media: med}

	groups, _, err := jpmTargetGroups(ctx, true)
	if err == nil && len(groups) > 0 {
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		success, failed := 0, 0
		for _, key := range keys {
			if err := jpmSendToGroup(ctx, groups[key].JID, content, false, nil); err != nil {
				failed++
			} else {
				success++
			}
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		pool.logger.Info().Int("success", success).Int("failed", failed).Msg("jpm: autojpm broadcast selesai")
	} else if err != nil {
		pool.logger.Warn().Err(err).Msg("jpm: autojpm gagal ambil daftar grup")
	}

	jpmSettingsMu.Lock()
	if jpmCfg.AutoJpm != nil {
		jpmCfg.AutoJpm.LastRun = time.Now().UnixMilli()
		jpmCfg.AutoJpm.NextRun = time.Now().Add(time.Duration(intervalMs) * time.Millisecond).UnixMilli()
	}
	jpmSettingsMu.Unlock()
	saveJpmSettings()
}

// ─── Delay ────────────────────────────────────────────────────────────────────

func handleJpmDelay(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ensureJpmSettings()
	input := strings.TrimSpace(args)
	if input == "" {
		jpmSettingsMu.Lock()
		current := jpmCfg.DelayMs
		jpmSettingsMu.Unlock()
		body := fmt.Sprintf("⏱️ *JPM Delay*\n\n"+
			"Atur jeda waktu antar pengiriman pesan ke setiap grup.\n"+
			"Semakin lama delay, semakin aman dari spam detection.\n\n"+
			"> Delay saat ini: *%dms* (*%.1f detik*)\n\n"+
			"*Pilih delay di bawah:*", current, float64(current)/1000)
		p := Prefix
		b := NewMsgBuilder().
			SetHeader(BotName+" Delay", fmt.Sprintf("by %s • prefix: %s", BotDeveloper, p)).
			SetBody(body).
			SetFooter(BotName + " JPM System").
			SetContextInfo(newsletterCtxInfo(ctx))
		for _, d := range []struct {
			label, desc string
			ms          int64
		}{
			{"⚡ 1 detik", "Sangat cepat, risiko spam tinggi", 1000},
			{"⚡ 2 detik", "Cepat, risiko spam sedang", 2000},
			{"⚡ 3 detik", "Standar, cukup aman", 3000},
			{"🕐 5 detik", "Aman, paling umum digunakan", 5000},
			{"🕐 7 detik", "Sangat aman", 7000},
			{"🕐 10 detik", "Paling aman dari spam", 10000},
			{"🕐 15 detik", "Untuk grup sangat banyak", 15000},
		} {
			b.AddQRButton(d.label, fmt.Sprintf("%sjpm _delay_%d", p, d.ms))
		}
		if err := b.Send(ctx, chat); err == nil {
			return
		}
		sendText(ctx, chat, body)
		return
	}

	ms, err := strconv.ParseInt(input, 10, 64)
	if err != nil || ms < 1000 || ms > 30000 {
		sendText(ctx, chat, "❌ Delay harus antara *1000ms* (1 detik) sampai *30000ms* (30 detik)")
		return
	}
	jpmSettingsMu.Lock()
	old := jpmCfg.DelayMs
	jpmCfg.DelayMs = ms
	jpmSettingsMu.Unlock()
	saveJpmSettings()
	sendText(ctx, chat, fmt.Sprintf("✅ *Delay JPM Diubah*\n\n"+
		"> Sebelumnya: *%dms* (*%.1f detik*)\n"+
		"> Sekarang: *%dms* (*%.1f detik*)\n\n"+
		"> Estimasi 100 grup: *%d menit*",
		old, float64(old)/1000, ms, float64(ms)/1000, int64(100*ms)/60000))
}

// ─── Blacklist ────────────────────────────────────────────────────────────────

func handleJpmBlacklist(ctx context.Context, evt *events.Message, args string, auto bool) {
	chat := evt.Info.Chat
	ensureJpmSettings()
	label := "JPM"
	cmdName := "bljpm"
	if auto {
		label = "AUTO JPM"
		cmdName = "blautojpm"
	}

	jpmSettingsMu.Lock()
	blacklist := append([]string{}, jpmCfg.Blacklist...)
	if auto {
		blacklist = append([]string{}, jpmCfg.AutoBlacklist...)
	}
	jpmSettingsMu.Unlock()

	groups, err := waClient.GetJoinedGroups(ctx)
	if err != nil {
		sendText(ctx, chat, "❌ Gagal ambil daftar grup: "+err.Error())
		return
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})

	if strings.TrimSpace(args) == "" {
		if len(groups) == 0 {
			sendText(ctx, chat, "❌ Bot belum tergabung di grup mana pun.")
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "📋 *Daftar Grup & %s Blacklist*\n\n"+
			"Berikut *%d grup* yang diikuti bot *%s*\n"+
			"Tanda *🚫* berarti grup sedang di-blacklist.\n\n",
			label, len(groups), BotName)
		for i, g := range groups {
			mark := ""
			if containsNumber(blacklist, g.JID.ToNonAD().String()) {
				mark = " 🚫"
			}
			fmt.Fprintf(&sb, "*%d.* %s%s\n", i+1, g.Name, mark)
		}
		fmt.Fprintf(&sb, "\n*CARA BLACKLIST / UN-BLACKLIST:*\n"+
			"Ketik command diikuti nomor grup (bisa lebih dari satu, pisahkan spasi).\n\n"+
			"*Contoh:*\n"+
			"> *%s%s 2 3 7*", Prefix, cmdName)
		sendText(ctx, chat, sb.String())
		return
	}

	var toggled []string
	for _, tok := range strings.Fields(args) {
		num, err := strconv.Atoi(tok)
		if err != nil || num < 1 || num > len(groups) {
			continue
		}
		g := groups[num-1]
		key := g.JID.ToNonAD().String()
		if containsNumber(blacklist, key) {
			blacklist = removeNumber(blacklist, key)
			toggled = append(toggled, fmt.Sprintf("*%d.* %s ✅ *(Di-Unblacklist)*", num, g.Name))
		} else {
			blacklist = append(blacklist, key)
			toggled = append(toggled, fmt.Sprintf("*%d.* %s 🚫 *(Di-Blacklist)*", num, g.Name))
		}
	}
	if len(toggled) == 0 {
		sendText(ctx, chat, fmt.Sprintf("❌ Tidak ada nomor grup yang valid.\n\n"+
			"Ketik *%s%s* untuk melihat daftar nomor.", Prefix, cmdName))
		return
	}

	jpmSettingsMu.Lock()
	if auto {
		jpmCfg.AutoBlacklist = blacklist
	} else {
		jpmCfg.Blacklist = blacklist
	}
	jpmSettingsMu.Unlock()
	saveJpmSettings()
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("📢 *%s Blacklist Diperbarui*\n\n%s", label, strings.Join(toggled, "\n")))
}

// ─── Help ─────────────────────────────────────────────────────────────────────

func jpmHelp(ctx context.Context, evt *events.Message) {
	p := Prefix
	sendText(ctx, evt.Info.Chat, fmt.Sprintf(
		"📢 *JPM — Sistem Broadcast Massal*\n\n"+
			"Sistem lengkap untuk mengirim pesan ke seluruh grup, channel, atau target tertentu secara otomatis maupun manual.\n\n"+
			"*CARA PAKAI:*\n"+
			"> Ketik *%sjpm* untuk membuka menu interaktif\n"+
			"> Bisa reply/kirim teks, foto, audio, atau video lalu ketik *%sjpm*\n"+
			"> Pilih mode pengiriman dari tombol yang muncul\n\n"+
			"*MODE BROADCAST:*\n"+
			"> 📢 *JPM Basic* — Kirim pesan ke semua grup tanpa tag\n"+
			"> 👁️ *JPM Hidetag* — Kirim pesan ke semua grup, tag tersembunyi\n"+
			"> 📺 *JPM Channel* — Kirim pesan ke semua channel newsletter\n"+
			"> 🚀 *JPM Update* — Broadcast changelog/update ke semua grup\n"+
			"> 🔄 *Auto JPM* — Atur jadwal siaran otomatis berdasar interval\n\n"+
			"*PENGATURAN:*\n"+
			"> ⏱️ *Atur Delay* — Jeda antar pengiriman per grup\n"+
			"> 🚫 *Blacklist JPM* — Kelola grup yang dikecualikan dari JPM\n"+
			"> 🚫 *Blacklist AutoJPM* — Kelola grup yang dikecualikan dari AutoJPM\n"+
			"> ⏹️ *Stop JPM* — Hentikan JPM yang sedang berjalan\n\n"+
			"*FORMAT INTERVAL:*\n"+
			"> *10m* (10 menit) • *1h* (1 jam) • *2h30m* (2 jam 30 menit) • *1d* (1 hari)",
		p, p))
}