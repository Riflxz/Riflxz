package main

// features_utilities.go — Fitur info & utilitas.
// Berisi: gempa (BMKG), ipinfo (ipwho.is), quran (nu.or.id),
// githubstalk (API GitHub resmi — bebas API key).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada: dlGetSafe, sendText, reactMsg, sendSearchImage.
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Gempa BMKG ──────────────────────────────────────────────────────────────

// handleGempa — info gempa terkini dari BMKG (port gempa.js).
// API publik: https://data.bmkg.go.id/DataMKG/TEWS/autogempa.json
func handleGempa(ctx context.Context, evt *events.Message) {
	reactMsg(ctx, evt, "🕕")
	body, err := dlGetSafe("https://data.bmkg.go.id/DataMKG/TEWS/autogempa.json")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mengambil data gempa: "+err.Error())
		return
	}
	var resp struct {
		Infogempa struct {
			Gempa struct {
				Tanggal     string `json:"Tanggal"`
				Jam         string `json:"Jam"`
				Coordinates string `json:"Coordinates"`
				Lintang     string `json:"Lintang"`
				Bujur       string `json:"Bujur"`
				Magnitude   string `json:"Magnitude"`
				Kedalaman   string `json:"Kedalaman"`
				Wilayah     string `json:"Wilayah"`
				Potensi     string `json:"Potensi"`
				Dirasakan   string `json:"Dirasakan"`
				Shakemap    string `json:"Shakemap"`
			} `json:"gempa"`
		} `json:"Infogempa"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Infogempa.Gempa.Wilayah == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Data gempa tidak tersedia.")
		return
	}
	g := resp.Infogempa.Gempa
	text := fmt.Sprintf(
		"🌍 *Info Gempa Terkini — BMKG*\n\n"+
			"> 📅 Tanggal: *%s*\n"+
			"> 🕐 Jam: *%s*\n"+
			"> 📐 Koordinat: *%s*\n"+
			"> 📍 Lintang: *%s*\n"+
			"> 📍 Bujur: *%s*\n"+
			"> 💥 Magnitude: *%s*\n"+
			"> 🔽 Kedalaman: *%s*\n"+
			"> 🗺️ Wilayah: *%s*\n"+
			"> ⚠️ Potensi: *%s*\n"+
			"> 🏠 Dirasakan: *%s*\n\n"+
			"_Sumber: BMKG Indonesia_",
		g.Tanggal, g.Jam, g.Coordinates, g.Lintang, g.Bujur,
		g.Magnitude, g.Kedalaman, g.Wilayah, g.Potensi, g.Dirasakan)

	// Kirim shakemap (peta guncangan) kalau ada — fallback ke teks.
	if g.Shakemap != "" {
		imgURL := "https://data.bmkg.go.id/DataMKG/TEWS/" + g.Shakemap
		if sendSearchImage(ctx, evt, imgURL, text) == nil {
			reactMsg(ctx, evt, "✅")
			return
		}
	}
	sendText(ctx, evt.Info.Chat, text)
	reactMsg(ctx, evt, "✅")
}

// ─── IP Info ─────────────────────────────────────────────────────────────────

// handleIPInfo — lookup info IP (port ipwho.js).
// API publik: https://ipwho.is/<ip> — tanpa API key.
func handleIPInfo(ctx context.Context, evt *events.Message, args string) {
	ip := strings.TrimSpace(args)
	if ip == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%sipinfo 8.8.8.8`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	body, err := dlGetSafe("https://ipwho.is/" + url.PathEscape(ip))
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal lookup IP: "+err.Error())
		return
	}
	var d struct {
		Success   bool    `json:"success"`
		IP        string  `json:"ip"`
		Country   string  `json:"country"`
		CountryCd string  `json:"country_code"`
		City      string  `json:"city"`
		Region    string  `json:"region"`
		Continent string  `json:"continent"`
		Postal    string  `json:"postal"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Timezone  struct {
			ID string `json:"id"`
		} `json:"timezone"`
		Connection struct {
			ISP string `json:"isp"`
			Org string `json:"org"`
			ASN int    `json:"asn"`
		} `json:"connection"`
		Security struct {
			VPN   bool `json:"vpn"`
			Proxy bool `json:"proxy"`
			Tor   bool `json:"tor"`
		} `json:"security"`
	}
	if json.Unmarshal(body, &d) != nil || !d.Success {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ IP *%s* tidak valid.", ip))
		return
	}
	text := fmt.Sprintf(
		"🌐 *IP LOOKUP*\n\n"+
			"🔢 IP: *%s*\n"+
			"🌍 Country: *%s* (%s)\n"+
			"🏙️ City: *%s*\n"+
			"📍 Region: *%s*\n"+
			"🌐 Continent: *%s*\n"+
			"📮 Postal: *%s*\n"+
			"⏰ Timezone: *%s*\n\n"+
			"🔌 ISP: *%s*\n"+
			"🌐 ORG: *%s*\n"+
			"📡 ASN: *%d*\n\n"+
			"🛡️ VPN: *%s* | Proxy: *%s* | Tor: *%s*",
		d.IP, d.Country, d.CountryCd, d.City, d.Region, d.Continent, d.Postal,
		d.Timezone.ID, d.Connection.ISP, d.Connection.Org, d.Connection.ASN,
		yesNo(d.Security.VPN), yesNo(d.Security.Proxy), yesNo(d.Security.Tor))
	sendText(ctx, evt.Info.Chat, text)
	reactMsg(ctx, evt, "✅")
}

func yesNo(b bool) string {
	if b {
		return "✅ Yes"
	}
	return "❌ No"
}

// ─── Rate (fun) ──────────────────────────────────────────────────────────────

var rateRatings = []struct{ score, comment string }{
	{"10/10", "Sempurna! Nggak ada duanya!"},
	{"9/10", "Hampir sempurna! Keren banget!"},
	{"8/10", "Bagus banget! Mantap!"},
	{"7/10", "Cukup bagus, di atas rata-rata!"},
	{"6/10", "Lumayan, bisa lebih baik lagi."},
	{"5/10", "Biasa aja sih, standar."},
	{"4/10", "Hmm, kurang sedikit."},
	{"3/10", "Perlu banyak perbaikan."},
	{"2/10", "Aduh, masih jauh dari bagus."},
	{"1/10", "Maaf, tapi ini parah."},
	{"100/10", "LEGEND! Beyond perfect!"},
	{"11/10", "Melebihi ekspektasi!"},
	{"69/100", "Nice..."},
	{"420/10", "BLAZING!"},
	{"∞/10", "Gacor kang"},
	{"7.5/10", "Solid! Good job!"},
	{"8.5/10", "Impressive!"},
	{"9.5/10", "Near perfection!"},
	{"-1/10", "Aku nggak tau harus ngomong apa..."},
	{"???/10", "Error 404: Rating not found."},
}

// handleRate — minta bot memberi rating sesuatu (port rate.js).
func handleRate(ctx context.Context, evt *events.Message, args string) {
	text := strings.TrimSpace(args)
	if text == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%srate wajahku`", Prefix))
		return
	}
	r := rateRatings[time.Now().UnixNano()%int64(len(rateRatings))]
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("Rating dari aku: *%s*\n%s", r.score, r.comment))
}

// ─── Cek Khodam (fun) ────────────────────────────────────────────────────────

var khodams = []struct{ name, meaning string }{
	{"Harimau Putih", "Kamu kuat dan berani seperti harimau, karena pendahulumu mewariskan kekuatan besar padamu."},
	{"Lampu Tertidur", "Terlihat ngantuk tapi selalu memberikan cahaya yang hangat"},
	{"Panda Ompong", "Kamu menggemaskan dan selalu berhasil membuat orang tersenyum dengan keanehanmu."},
	{"Bebek Karet", "Kamu selalu tenang dan ceria, mampu menghadapi gelombang masalah dengan senyum."},
	{"Ninja Turtle", "Kamu lincah dan tangguh, siap melindungi yang lemah dengan kekuatan tempurmu."},
	{"Kucing Kulkas", "Kamu misterius dan selalu ada di tempat-tempat yang tak terduga."},
	{"Sabun Wangi", "Kamu selalu membawa keharuman dan kesegaran di mana pun kamu berada."},
	{"Semut Kecil", "Kamu pekerja keras dan selalu bisa diandalkan dalam situasi apa pun."},
	{"Cupcake Pelangi", "Kamu manis dan penuh warna, selalu membawa kebahagiaan dan keceriaan."},
	{"Robot Mini", "Kamu canggih dan selalu siap membantu dengan kecerdasan teknologi tinggi."},
	{"Ikan Terbang", "Kamu unik dan penuh kejutan, selalu melampaui batasan yang ada."},
	{"Ayam Goreng", "Kamu selalu disukai dan dinanti oleh banyak orang, penuh kelezatan dalam setiap langkahmu."},
	{"Kecoa Terbang", "Kamu selalu mengagetkan dan bikin heboh seisi ruangan."},
	{"Kambing Ngebor", "Kamu unik dan selalu bikin orang tertawa dengan tingkah lakumu yang aneh."},
	{"Kerupuk Renyah", "Kamu selalu bikin suasana jadi lebih seru dan nikmat."},
	{"Celengan Babi", "Kamu selalu menyimpan kejutan di dalam dirimu."},
	{"Lemari Tua", "Kamu penuh dengan cerita dan kenangan masa lalu."},
	{"Kopi Susu", "Kamu manis dan selalu bikin semangat orang-orang di sekitarmu."},
	{"Sapu Lidi", "Kamu kuat dan selalu bisa diandalkan untuk membersihkan masalah."},
	{"Indomie Goreng", "Selalu bikin kenyang dan bahagia"},
	{"Es Krim Meleleh", "Selalu mencairkan suasana dengan rasa manisnya"},
	{"Bakso Ulet", "Selalu gigih dan bulat dalam menghadapi masalah"},
	{"Lem Super", "Selalu lengket dalam situasi yang rumit"},
	{"Kecap Manis", "Selalu memberikan sentuhan manis dalam hidup"},
	{"Sabun Mandi", "Selalu bersih dan wangi"},
	{"Kopi Tumpah", "Selalu bersemangat, tapi kadang berantakan"},
	{"Kucing Kampung", "Selalu mandiri dan penuh petualangan"},
	{"Jamu Pahit", "Selalu memberi kekuatan meski tak enak di awal"},
	{"Teh Celup", "Selalu memberikan rasa hangat di hati"},
	{"Motor Astrea", "Selalu setia dan bandel"},
	{"Mie Instan", "Selalu cepat dan mengenyangkan"},
	{"Bolu Kukus", "Selalu lembut dan manis"},
	{"Tahu Bulat", "Selalu enak di segala suasana"},
	{"Nasi Uduk", "Selalu cocok di segala waktu"},
	{"Singa Bermahkota", "Kamu lahir sebagai pemimpin, memiliki kekuatan dan kebijaksanaan seorang raja."},
	{"Macan Kumbang", "Kamu misterius dan kuat, seperti macan yang jarang terlihat tapi selalu waspada."},
	{"Kuda Emas", "Kamu berharga dan kuat, siap untuk berlari menuju kesuksesan."},
	{"Elang Biru", "Kamu memiliki visi yang tajam dan dapat melihat peluang dari jauh."},
	{"Naga Pelangi", "Kamu tangguh dan memiliki kekuatan untuk melindungi dan menyerang."},
	{"Gajah Putih", "Kamu bijaksana dan memiliki kekuatan besar, lambang dari keberanian dan keteguhan hati."},
	{"Banteng Sakti", "Kamu kuat dan penuh semangat, tidak takut menghadapi rintangan."},
	{"Kipas Angin", "Selalu memberikan angin segar"},
	{"Rice Cooker", "Selalu memasak nasi dengan sempurna"},
	{"Honda Beat", "Selalu lincah di jalanan"},
	{"Sandal Jepit", "Selalu santai dan nyaman"},
	{"Bantal Guling", "Selalu nyaman di pelukan"},
	{"Anjing Pelacak", "Kamu setia dan penuh dedikasi, selalu menemukan jalan menuju tujuanmu."},
}

// handleCekKhodam — cek khodam diri sendiri atau orang lain (port cekkhodam.js).
// Versi SC: teks saja (tanpa TTS) — lebih ringan & cepat.
func handleCekKhodam(ctx context.Context, evt *events.Message, args string) {
	targetName := strings.TrimSpace(args)
	if targetName == "" {
		targetName = evt.Info.Sender.User
	}
	k := khodams[time.Now().UnixNano()%int64(len(khodams))]
	sendText(ctx, evt.Info.Chat, fmt.Sprintf(
		"Halo kak *%s*, Khodam kamu adalah *%s*.\n\nKhodam ini memiliki arti: %s",
		targetName, k.name, k.meaning))
}

// ─── Quran (nu.or.id) ────────────────────────────────────────────────────────

// quranAyat — satu ayat hasil parse halaman quran.nu.or.id.
type quranAyat struct {
	Ayat  int
	Arab  string
	Latin string
	Arti  string
}

var (
	quranDivRe   = regexp.MustCompile(`<div[^>]*\sid="(\d+)"[^>]*>`)
	quranArabRe  = regexp.MustCompile(`dir="rtl">([^<]+)`)
	quranLatinRe = regexp.MustCompile(`text-primary-500[^>]*>([^<]+)`)
	quranArtiRe  = regexp.MustCompile(`text-neutral-700[^>]*>([^<]+)`)
)

// fetchQuranSurah — scrape halaman quran.nu.or.id/<slug> (port quran.js).
// Tanpa HTML parser: regex cukup karena struktur halamannya stabil.
func fetchQuranSurah(slug string) (title string, ayat []quranAyat, err error) {
	body, err := dlGetSafe("https://quran.nu.or.id/" + url.PathEscape(slug))
	if err != nil {
		return "", nil, err
	}
	html := string(body)

	// Judul surah dari <h1>.
	if m := regexp.MustCompile(`<h1[^>]*>([^<]+)</h1>`).FindStringSubmatch(html); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}

	// Pecah per ayat: setiap <div id="N"> ... </div> berisi arab, latin, arti.
	// Ambil potongan dari tiap div id sampai div id berikutnya (atau akhir).
	matches := quranDivRe.FindAllStringSubmatchIndex(html, -1)
	for i, m := range matches {
		start := m[1]
		end := len(html)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		seg := html[start:end]
		arab := firstMatch(quranArabRe, seg)
		latin := firstMatch(quranLatinRe, seg)
		arti := firstMatch(quranArtiRe, seg)
		if arab == "" || latin == "" || arti == "" {
			continue
		}
		var num int
		fmt.Sscanf(html[m[2]:m[3]], "%d", &num)
		ayat = append(ayat, quranAyat{Ayat: num, Arab: arab, Latin: latin, Arti: arti})
	}
	if len(ayat) == 0 {
		return "", nil, fmt.Errorf("surah tidak ditemukan")
	}
	return title, ayat, nil
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// handleQuran — baca ayat Al-Quran berdasarkan nama surah (port quran.js).
func handleQuran(ctx context.Context, evt *events.Message, args string) {
	query := strings.TrimSpace(args)
	if query == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%squran al fatihah`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	slug := strings.ReplaceAll(strings.ToLower(query), " ", "-")
	title, ayat, err := fetchQuranSurah(slug)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Surah tidak ditemukan: "+err.Error())
		return
	}
	if title == "" {
		title = strings.Title(query)
	}
	// Kirim maksimal 10 ayat pertama biar pesan tidak kepanjangan.
	var b strings.Builder
	fmt.Fprintf(&b, "📖 *%s*\n\n", title)
	for i, a := range ayat {
		if i >= 10 {
			fmt.Fprintf(&b, "\n_…dan %d ayat lainnya. Ketik `%squran %s` untuk surah lain._",
				len(ayat)-10, Prefix, slug)
			break
		}
		fmt.Fprintf(&b, "*%d.* %s\n_%s_\n%s\n\n", a.Ayat, a.Arab, a.Latin, a.Arti)
	}
	sendText(ctx, evt.Info.Chat, b.String())
	reactMsg(ctx, evt, "✅")
}

// ─── GitHub Stalk ────────────────────────────────────────────────────────────

// handleGitHubStalk — stalk akun GitHub (port githubstalk.js).
// Dipakai API GitHub resmi (api.github.com/users/<username>) — bebas API key,
// tidak seperti versi asli yang butuh apikey firefly.
func handleGitHubStalk(ctx context.Context, evt *events.Message, args string) {
	username := strings.TrimSpace(strings.TrimPrefix(args, "@"))
	if username == "" {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%sgithubstalk torvalds`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")
	body, err := dlGetSafe("https://api.github.com/users/" + url.PathEscape(username))
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Username *%s* tidak ditemukan.", username))
		return
	}
	var d struct {
		Login       string `json:"login"`
		Name        string `json:"name"`
		Company     string `json:"company"`
		Location    string `json:"location"`
		PublicRepos int    `json:"public_repos"`
		Followers   int    `json:"followers"`
		Following   int    `json:"following"`
		Bio         string `json:"bio"`
		AvatarURL   string `json:"avatar_url"`
		HTMLURL     string `json:"html_url"`
	}
	if json.Unmarshal(body, &d) != nil || d.Login == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Username *%s* tidak ditemukan.", username))
		return
	}
	caption := fmt.Sprintf(
		"🐙 *GITHUB STALK*\n\n"+
			"👤 *Username:* %s\n"+
			"📛 *Nama:* %s\n"+
			"🏢 *Company:* %s\n"+
			"📍 *Location:* %s\n\n"+
			"📦 *Public Repos:* %d\n"+
			"👥 *Followers:* %d\n"+
			"👤 *Following:* %d\n\n"+
			"📝 *Bio:*\n%s\n\n"+
			"🔗 %s",
		d.Login, orDash(d.Name), orDash(d.Company), orDash(d.Location),
		d.PublicRepos, d.Followers, d.Following, orDash(d.Bio), d.HTMLURL)
	if d.AvatarURL != "" && sendSearchImage(ctx, evt, d.AvatarURL, caption) == nil {
		reactMsg(ctx, evt, "✅")
		return
	}
	sendText(ctx, evt.Info.Chat, caption)
	reactMsg(ctx, evt, "✅")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}