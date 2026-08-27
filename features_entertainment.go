package main

// features_entertainment.go — Fitur hiburan & utilitas harian.
// Berisi: jadwalsholat (myquran.com), berita (RSS), npmstalk (npm registry resmi),
// ceknik (parse NIK lokal), emojitoimage (Noto Emoji CDN), tiktokstalk
// (siputzx stalk API), howgay (random), mediafire (scrape HTML).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada: dlGetSafe, sendText, reactMsg, sendImage.
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Jadwal Sholat ────────────────────────────────────────────────────────────
// API: myquran.com (publik, tanpa key) — port dari religi/jadwalsholat.js.
// Cmd: !jadwalsholat <kota>, !sholat, !prayertime

type myquranKota struct {
	ID     string `json:"id"`
	Lokasi string `json:"lokasi"`
}

type myquranJadwal struct {
	Jadwal struct {
		Imsak   string `json:"imsak"`
		Subuh   string `json:"subuh"`
		Terbit  string `json:"terbit"`
		Dhuha   string `json:"dhuha"`
		Dzuhur  string `json:"dzuhur"`
		Ashar   string `json:"ashar"`
		Maghrib string `json:"maghrib"`
		Isya    string `json:"isya"`
	} `json:"jadwal"`
	Lokasi string `json:"lokasi"`
	Daerah string `json:"daerah"`
}

func handleJadwalSholat(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	city := strings.TrimSpace(args)
	if city == "" {
		city = "Jakarta"
	}
	reactMsg(ctx, evt, "🕌")

	// 1. Cari kota
	kotaURL := "https://api.myquran.com/v2/sholat/kota/cari/" + url.QueryEscape(city)
	body, err := dlGetSafe(kotaURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi API jadwal sholat.")
		return
	}
	var kotaResp struct {
		Status bool          `json:"status"`
		Data   []myquranKota `json:"data"`
	}
	if json.Unmarshal(body, &kotaResp) != nil || !kotaResp.Status || len(kotaResp.Data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Kota *%s* tidak ditemukan. Coba nama kabupaten/kota lain.", city))
		return
	}
	kota := kotaResp.Data[0]

	// 2. Jadwal hari ini (waktu Asia/Jakarta)
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Now().In(loc)
	jadwalURL := fmt.Sprintf("https://api.myquran.com/v2/sholat/jadwal/%s/%d/%02d/%02d",
		kota.ID, now.Year(), int(now.Month()), now.Day())
	body, err = dlGetSafe(jadwalURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil jadwal sholat.")
		return
	}
	var jadwalResp struct {
		Status bool          `json:"status"`
		Data   myquranJadwal `json:"data"`
	}
	if json.Unmarshal(body, &jadwalResp) != nil || !jadwalResp.Status {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Data jadwal sholat tidak tersedia.")
		return
	}
	j := jadwalResp.Data.Jadwal
	tanggal := now.Format("Monday, 02 January 2006")

	text := fmt.Sprintf(
		"🕌 *Jadwal Sholat*\n\n"+
			"> 📍 Lokasi: *%s*\n"+
			"> 📅 %s\n\n"+
			"╔═ ⏰ *WAKTU SHOLAT*\n"+
			"║ 🌙 Imsak: `%s`\n"+
			"║ 🌅 Subuh: `%s`\n"+
			"║ ☀️ Terbit: `%s`\n"+
			"║ 🌤️ Dhuha: `%s`\n"+
			"║ 🌞 Dzuhur: `%s`\n"+
			"║ 🌇 Ashar: `%s`\n"+
			"║ 🌆 Maghrib: `%s`\n"+
			"║ 🌃 Isya: `%s`\n"+
			"╚════════\n\n"+
			"_Sumber: myquran.com_",
		jadwalResp.Data.Lokasi, tanggal,
		j.Imsak, j.Subuh, j.Terbit, j.Dhuha, j.Dzuhur, j.Ashar, j.Maghrib, j.Isya)

	sendText(ctx, chat, text)
	reactMsg(ctx, evt, "✅")
}

// ─── Berita RSS ───────────────────────────────────────────────────────────────
// RSS publik — port dari info/berita.js. Tanpa lib XML eksternal (encoding/xml).
// Cmd: !berita [sumber], !antara, !cnn, !cnbc, !sindonews

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

type rssFeed struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

var newsSources = []struct {
	Key  string
	Name string
	Emoji string
	URL  string
}{
	{"antara", "Antara News", "📰", "https://www.antaranews.com/rss/terkini.xml"},
	{"cnn", "CNN Indonesia", "📺", "https://www.cnnindonesia.com/nasional/rss"},
	{"cnbc", "CNBC Indonesia", "💹", "https://www.cnbcindonesia.com/rss"},
	{"sindonews", "Sindo News", "📰", "https://international.sindonews.com/rss"},
}

var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

func handleNewsRSS(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	source := strings.ToLower(strings.TrimSpace(args))

	// Tanpa argumen → daftar sumber.
	if source == "" {
		var b strings.Builder
		b.WriteString("📰 *Daftar Sumber Berita*\n\n")
		for _, s := range newsSources {
			fmt.Fprintf(&b, "%s `%s%s` — %s\n", s.Emoji, Prefix, s.Key, s.Name)
		}
		fmt.Fprintf(&b, "\n_Atau: `%sberita <sumber>`_", Prefix)
		sendText(ctx, chat, b.String())
		return
	}

	var src *struct {
		Key  string
		Name string
		Emoji string
		URL  string
	}
	for i := range newsSources {
		if newsSources[i].Key == source {
			src = &newsSources[i]
			break
		}
	}
	if src == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Sumber *%s* tidak ditemukan. Ketik `%sberita` untuk daftar sumber.", source, Prefix))
		return
	}

	reactMsg(ctx, evt, "🕕")
	body, err := dlGetSafe(src.URL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil berita.")
		return
	}
	var feed rssFeed
	if xml.Unmarshal(body, &feed) != nil || len(feed.Channel.Items) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak ada berita ditemukan.")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s *%s*\n", src.Emoji, strings.ToUpper(src.Name))
	b.WriteString("━━━━━━━━━━━━━━━\n\n")
	for i, item := range feed.Channel.Items {
		if i >= 7 {
			break
		}
		desc := strings.TrimSpace(reHTMLTag.ReplaceAllString(item.Description, ""))
		if len(desc) > 150 {
			desc = desc[:150]
		}
		fmt.Fprintf(&b, "*%d. %s*\n", i+1, item.Title)
		if desc != "" {
			fmt.Fprintf(&b, "%s...\n", desc)
		}
		fmt.Fprintf(&b, "🔗 %s\n", item.Link)
		if item.PubDate != "" {
			fmt.Fprintf(&b, "📅 _%s_\n", item.PubDate)
		}
		b.WriteString("\n")
	}
	b.WriteString("━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "_Total: %d artikel tersedia_", len(feed.Channel.Items))

	sendText(ctx, chat, b.String())
	reactMsg(ctx, evt, "📰")
}

// ─── NPM Stalk ────────────────────────────────────────────────────────────────
// API: registry npm resmi (tanpa key) — port dari stalker/npmstalk.js.
// Versi asli pakai firefly (butuh key) — diganti API resmi.
// Cmd: !npmstalk <username>

type npmSearchResp struct {
	Objects []struct {
		Package struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			Description string `json:"description"`
		} `json:"package"`
		Downloads struct {
			Monthly int `json:"monthly"`
		} `json:"downloads"`
	} `json:"objects"`
}

func shortNum(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

func handleNPMStalk(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	username := strings.TrimSpace(args)
	if username == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%snpmstalk hanya_zann`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	// Cari package yang di-maintain username tsb.
	apiURL := "https://registry.npmjs.org/-/v1/search?text=" + url.QueryEscape("maintainer:"+username) + "&size=10"
	body, err := dlGetSafe(apiURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi registry npm.")
		return
	}
	var resp npmSearchResp
	if json.Unmarshal(body, &resp) != nil || len(resp.Objects) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Username *%s* tidak ditemukan di npm.", username))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📦 *NPM Stalk*\n\n")
	fmt.Fprintf(&b, "👤 *Username:* %s\n", username)
	fmt.Fprintf(&b, "📦 *Total packages:* %d\n\n", len(resp.Objects))
	b.WriteString("*Daftar Package:*\n")
	for i, o := range resp.Objects {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&b, "> 📦 *%s* (v%s)\n", o.Package.Name, o.Package.Version)
		fmt.Fprintf(&b, "> 📉 %s dl/month\n", shortNum(o.Downloads.Monthly))
		if o.Package.Description != "" {
			fmt.Fprintf(&b, "> 📝 %s\n", o.Package.Description)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "🔗 https://www.npmjs.com/~%s", username)

	sendText(ctx, chat, b.String())
	reactMsg(ctx, evt, "✅")
}

// ─── Cek NIK ──────────────────────────────────────────────────────────────────
// Parse NIK lokal (logika murni, tanpa API) — port dari tools/nikparser.js.
// Versi asli pakai API obscura (butuh key) — diganti parse mandiri.
// Cmd: !ceknik <16 digit>

var provinsiNIK = map[string]string{
	"11": "Aceh", "12": "Sumatera Utara", "13": "Sumatera Barat", "14": "Riau",
	"15": "Jambi", "16": "Sumatera Selatan", "17": "Bengkulu", "18": "Lampung",
	"19": "Kepulauan Bangka Belitung", "21": "Kepulauan Riau", "31": "DKI Jakarta",
	"32": "Jawa Barat", "33": "Jawa Tengah", "34": "DI Yogyakarta", "35": "Jawa Timur",
	"36": "Banten", "51": "Bali", "52": "Nusa Tenggara Barat", "53": "Nusa Tenggara Timur",
	"61": "Kalimantan Barat", "62": "Kalimantan Tengah", "63": "Kalimantan Selatan",
	"64": "Kalimantan Timur", "65": "Kalimantan Utara", "71": "Sulawesi Utara",
	"72": "Sulawesi Tengah", "73": "Sulawesi Selatan", "74": "Sulawesi Tenggara",
	"75": "Gorontalo", "76": "Sulawesi Barat", "81": "Maluku", "82": "Maluku Utara",
	"91": "Papua", "92": "Papua Barat",
}

func handleCekNIK(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	nik := regexp.MustCompile(`\D`).ReplaceAllString(args, "")

	if len(nik) != 16 {
		sendText(ctx, chat, fmt.Sprintf(
			"🪪 *NIK Parser*\n\n"+
				"> Parse & validasi NIK KTP 🇮🇩\n"+
				"> Masukkan 16 digit angka NIK\n\n"+
				"`%sceknik 3517072109020003`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🕕")

	prov := provinsiNIK[nik[0:2]]
	if prov == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "🪪 *NIK tidak valid*\n\n> Kode provinsi tidak dikenal.")
		return
	}

	// Tanggal lahir: 6 digit (DDMMYY) — wanita: tanggal +40.
	day, _ := strconv.Atoi(nik[6:8])
	month, _ := strconv.Atoi(nik[8:10])
	year, _ := strconv.Atoi(nik[10:12])
	gender := "Pria"
	if day > 40 {
		day -= 40
		gender = "Wanita"
	}
	if day < 1 || day > 31 || month < 1 || month > 12 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "🪪 *NIK tidak valid*\n\n> Tanggal lahir tidak masuk akal.")
		return
	}
	yearFull := 2000 + year
	if yearFull > time.Now().Year() {
		yearFull = 1900 + year
	}
	tglLahir := fmt.Sprintf("%02d %s %d", day, monthNameID(month), yearFull)

	text := fmt.Sprintf(
		"🪪 *NIK Parser*\n\n"+
			"> *NIK* → `%s`\n"+
			"> *Valid* → ✅ Valid\n"+
			"> *Tanggal Lahir* → %s\n"+
			"> *Jenis Kelamin* → %s\n"+
			"> *Provinsi* → %s\n"+
			"> *Kab/Kota* → Kode `%s`\n"+
			"> *Kecamatan* → Kode `%s`\n"+
			"> *Kode Unik* → `%s`",
		nik, tglLahir, gender, prov, nik[2:4], nik[4:6], nik[12:16])

	sendText(ctx, chat, text)
	reactMsg(ctx, evt, "✅")
}

func monthNameID(m int) string {
	names := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni",
		"Juli", "Agustus", "September", "Oktober", "November", "Desember"}
	if m < 1 || m > 12 {
		return "?"
	}
	return names[m]
}

// ─── Emoji to Image ───────────────────────────────────────────────────────────
// Noto Emoji CDN (Google, gratis) — port dari tools/emojitoimage.js.
// Versi asli pakai neoxr (butuh key) — diganti CDN publik.
// Cmd: !emojitoimage <emoji> [style]

var emojiStyles = map[string]string{
	"google":    "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
	"twitter":   "https://cdn.jsdelivr.net/gh/jdecked/twemoji@latest/assets/72x72/%s.png",
	"whatsapp":  "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
	"apple":     "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
	"microsoft": "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
	"samsung":   "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
	"facebook":  "https://raw.githubusercontent.com/googlefonts/noto-emoji/main/png/512/emoji_u%s.png",
}

func handleEmojiToImage(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) == 0 {
		sendText(ctx, chat, fmt.Sprintf(
			"🖼️ *Emoji to Image*\n\n"+
				"> Konversi emoji ke gambar HD\n\n"+
				"*Format:*\n"+
				"> `%semojitoimage <emoji> [style]`\n\n"+
				"*Contoh:*\n"+
				"> `%semojitoimage 😳 apple`\n\n"+
				"*Style:* google, twitter, apple, whatsapp, microsoft, samsung, facebook",
			Prefix, Prefix))
		return
	}
	emoji := fields[0]
	style := "google"
	if len(fields) > 1 {
		style = strings.ToLower(fields[1])
	}
	tpl, ok := emojiStyles[style]
	if !ok {
		style = "google"
		tpl = emojiStyles[style]
	}

	reactMsg(ctx, evt, "🖼️")

	// Emoji → codepoints hex (underscore-separated untuk Noto, dash untuk Twemoji).
	var cps []string
	for _, r := range emoji {
		cps = append(cps, fmt.Sprintf("%x", r))
	}
	sep := "_"
	if style == "twitter" {
		sep = "-"
	}
	imgURL := fmt.Sprintf(tpl, strings.Join(cps, sep))

	data, err := dlGetSafe(imgURL)
	if err != nil || len(data) < 100 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Emoji tidak ditemukan atau gagal mengambil gambar.")
		return
	}
	if err := sendImage(ctx, chat, data, fmt.Sprintf(
		"🖼️ Emoji to Image\n\n> Emoji: %s\n> Style: %s", emoji, style)); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim gambar: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── TikTok Stalk ─────────────────────────────────────────────────────────────
// API: siputzx /api/stalk/tiktok — port dari stalker/tiktokstalk.js.
// Pengganti TikWM.com yang kena 403 (Cloudflare) + fallback shannz yang
// secret key-nya sudah invalid.
// Cmd: !tiktokstalk <username>

// tikwmUserInfo — ambil info user TikTok via siputzx stalk/tiktok.
// Endpoint-nya flaky (503 "All nodes failed") — retry 3x via siputzxGet.
func tikwmUserInfo(username string) ([]byte, error) {
	return siputzxGet("/api/stalk/tiktok?username=" + url.QueryEscape(username))
}

type tikwmUserResp struct {
	Status bool `json:"status"`
	Data   struct {
		User struct {
			UniqueID     string `json:"uniqueId"`
			Nickname     string `json:"nickname"`
			AvatarMedium string `json:"avatarMedium"`
			Signature    string `json:"signature"`
			Verified     bool   `json:"verified"`
			Private      bool   `json:"privateAccount"`
		} `json:"user"`
	} `json:"data"`
}

func handleTikTokStalk(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	username := strings.TrimPrefix(strings.TrimSpace(args), "@")
	if username == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%stiktokstalk mrbeast`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	body, err := tikwmUserInfo(username)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi API TikTok.")
		return
	}
	var resp tikwmUserResp
	if json.Unmarshal(body, &resp) != nil || !resp.Status || resp.Data.User.UniqueID == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Username *@%s* tidak ditemukan.", username))
		return
	}
	u := resp.Data.User

	verified := "Tidak"
	if u.Verified {
		verified = "Ya"
	}
	private := "Tidak"
	if u.Private {
		private = "Ya"
	}
	bio := u.Signature
	if bio == "" {
		bio = "-"
	}

	// Dua varian: plain untuk caption media, rich untuk fallback sendText.
	plain := fmt.Sprintf(
		"🎵 TikTok Stalk\n\n"+
			"👤 Username: @%s\n"+
			"📛 Nama: %s\n"+
			"✅ Verified: %s\n"+
			"🔒 Private: %s\n\n"+
			"📝 Bio:\n%s\n\n"+
			"🔗 https://tiktok.com/@%s",
		u.UniqueID, u.Nickname, verified, private, bio, u.UniqueID)
	rich := fmt.Sprintf(
		"🎵 *TikTok Stalk*\n\n"+
			"👤 *Username:* @%s\n"+
			"📛 *Nama:* %s\n"+
			"✅ *Verified:* %s\n"+
			"🔒 *Private:* %s\n\n"+
			"📝 *Bio:*\n%s\n\n"+
			"🔗 https://tiktok.com/@%s",
		u.UniqueID, u.Nickname, verified, private, bio, u.UniqueID)

	if u.AvatarMedium != "" {
		if img, err := dlGetSafe(u.AvatarMedium); err == nil && len(img) > 100 {
			if sendImage(ctx, chat, img, plain) == nil {
				reactMsg(ctx, evt, "✅")
				return
			}
		}
	}
	sendText(ctx, chat, rich)
	reactMsg(ctx, evt, "✅")
}

// ─── HowGay ───────────────────────────────────────────────────────────────────
// Random persen — port dari fun/gay.js (versi SC: untuk user yang dimention
// atau sender, tanpa perlu group metadata).
// Cmd: !howgay [@mention]

func handleHowGay(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	target := strings.TrimSpace(args)
	if target == "" {
		target = "kamu"
	}
	// Random 0-100 dengan seed waktu supaya beda tiap kali.
	percent := time.Now().UnixNano()%101
	label := "🤔"
	switch {
	case percent < 10:
		label = "😇"
	case percent < 30:
		label = "🙂"
	case percent < 60:
		label = "😏"
	case percent < 85:
		label = "🌈"
	default:
		label = "🏳️‍🌈"
	}
	sendText(ctx, chat, fmt.Sprintf(
		"🏳️‍🌈 *How Gay*\n\n> %s: *%d%%* gay %s", target, percent, label))
}

// ─── MediaFire Download ───────────────────────────────────────────────────────
// Scrape HTML — port dari download/mediafiredl.js (scraper mediafire.js).
// Cmd: !mediafire <url>, !mfdl, !mf

var reMediaFireURL = regexp.MustCompile(`(?i)mediafire\.com`)

func handleMediaFire(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	rawURL := strings.TrimSpace(args)
	if rawURL == "" || !reMediaFireURL.MatchString(rawURL) {
		sendText(ctx, chat, fmt.Sprintf(
			"⚠️ *MediaFire Download*\n\n"+
				"> `%smfdl <url mediafire>`\n\n"+
				"> Contoh:\n"+
				"> `%smfdl https://www.mediafire.com/file/xxx`",
			Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "🕕")

	html, err := dlGetSafe(rawURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil halaman MediaFire.")
		return
	}
	doc := string(html)

	// Title dari og:title / <title>.
	title := ""
	if m := regexp.MustCompile(`(?i)<meta[^>]+property="og:title"[^>]+content="([^"]*)"`).FindStringSubmatch(doc); len(m) > 1 {
		title = m[1]
	} else if m := regexp.MustCompile(`(?i)<title[^>]*>([^<]*)</title>`).FindStringSubmatch(doc); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}
	if title == "" {
		title = "MediaFire File"
	}

	// Link download: #downloadButton href atau a[aria-label="Download file"].
	link := ""
	if m := regexp.MustCompile(`(?i)id="downloadButton"[^>]*href="([^"]*)"`).FindStringSubmatch(doc); len(m) > 1 {
		link = m[1]
	} else if m := regexp.MustCompile(`(?i)aria-label="Download file"[^>]*href="([^"]*)"`).FindStringSubmatch(doc); len(m) > 1 {
		link = m[1]
	}
	if link == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Link download tidak ditemukan di halaman itu.")
		return
	}

	// Ukuran dari teks tombol: "(12.3 MB)" — cari pola ukuran file saja.
	size := ""
	if m := regexp.MustCompile(`\(([\d.,]+\s*(?:KB|MB|GB))\)`).FindStringSubmatch(doc); len(m) > 1 {
		size = m[1]
	}

	sendText(ctx, chat, fmt.Sprintf(
		"📦 *MediaFire*\n\n"+
			"> 📄 Nama: *%s*\n"+
			"> 📏 Ukuran: *%s*\n\n"+
			"🔗 Link download:\n%s",
		title, size, link))
	reactMsg(ctx, evt, "✅")
}