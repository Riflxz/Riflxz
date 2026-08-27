package main

// bulk_alight.go — Bulk generator akun Alight Motion premium (API mtzhacie).
// Command (gratis untuk semua user):
//
//	!bulk 1  → 1 akun (max 1 per command)
//
// Alur per akun (dua tahap — user yang login sendiri):
//
//	Tahap 1 — aktifasi:
//	  1. generate email sementara mtc.<random>@mtcommunty.web.id (inbox generator.email)
//	  2. POST /send-link → magic link #1 dikirim ke inbox email tsb
//	  3. polling inbox sampai link #1 muncul
//	  4. POST /aktifasi {email, link1} → server yang sign-in + verifyPurchase +
//	     tulis license → akun aktif premium (link #1 terpakai server, sekali pakai)
//	  5. kirim EMAIL saja ke user — akun sudah premium, tinggal login
//
//	Tahap 2 — link fresh setelah user login:
//	  6. USER buka app/situs Alight Motion & login pakai email tsb → Firebase
//	     kirim magic link BARU (fresh) ke inbox
//	  7. bot polling inbox, tangkap link baru itu → kirim ke user → selesai
//
//	Alasan 2 tahap: link yang dikirim ke user harus fresh + baru (hasil login
//	user), biar user bisa masuk dan tidak error — link #1 sudah terpakai server.
//
// Inbox generator.email (format 2026-08):
//
//	GET /inbox1/ + cookie `inbox_ctx=domain%2Fuser` (URL-encoded, slash = %2F)
//	  → halaman berisi SITE_DATA (num_mess, mess_id_raw, cur_msg_id) dan:
//	    - 1 pesan  : isi pesan (body) dirender langsung di #email-table
//	    - 2+ pesan : daftar item, tiap item onclick="loadInboxClientSide('domain/user/MSGID')"
//	  → msgID didapat dari `data-mid="..."` (mode 1 pesan) atau onclick (mode daftar)
//	Baca pesan spesifik: GET /inbox1/ + cookie `inbox_ctx=domain%2Fuser%2FMSGID`
//	  → body pesan itu dirender di halaman, magic link bisa diextract.
//	JANGAN pakai ?src=<mid> — butuh CAPTCHA. JANGAN pakai /inbox1/domain/user/
//	  (redirect ke /inbox7/ client-side).
//
// Endpoint (mtzhacie):
//
//	POST /send-link  {"email": "..."}
//	POST /aktifasi   {"email": "...", "link": "..."}
//
// Header: X-API-Key + Content-Type: application/json

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	alightEmailDomain = "mtcommunty.web.id"
	alightInboxBase   = "https://generator.email"
	// Polling tahap 1 (link aktivasi): cepat, link biasanya masuk <1 menit.
	alightPollMax   = 20
	alightPollDelay = 3 * time.Second
	// Timeout WebSocket notificon tahap 1 (sama dengan WS_TIMEOUT_MS asli):
	// kalau WS gagal, polling halaman tetap jalan sebagai fallback.
	alightWSTimeout = 45 * time.Second
	// Polling tahap 2 (link setelah user login): user butuh waktu buka app,
	// jadi lebih lama (5 menit), tapi async (safeGo) tidak memblokir command lain.
	alightBulkPollMax   = 60
	alightBulkPollDelay = 5 * time.Second

	// Anti-spam !bulk: cooldown per user + hanya 1 proses berjalan global.
	bulkCooldownTime = 2 * time.Minute
)

var (
	// extractOobLink asli (generator.js): ambil SEMUA URL generik dulu, lalu
	// pilih prioritas → firebase links → alight.link → oobCode.
	alightAnyURLRe = regexp.MustCompile(`https?://[^\s"'<>\\]+`)
	alightOobRe    = regexp.MustCompile(`(?i)oobcode|oob_code`)

	alightFirebaseLinkRe = regexp.MustCompile(`https://alight-creative\.firebaseapp\.com/__/auth/links[^"'\s<]*`)
	alightLinkRe         = regexp.MustCompile(`https://alight\.link/[^"'\s<]*`)
	// jumlah pesan di inbox: SITE_DATA ... num_mess:(\d+)
	alightNumMessRe = regexp.MustCompile(`num_mess:(\d+)`)
	// msgID dari mode daftar (2+ pesan): onclick="loadInboxClientSide('domain/user/MSGID')"
	alightListMsgIDRe = regexp.MustCompile(`loadInboxClientSide\('[^']*/([A-Za-z0-9_.-]+)'\)`)
	// msgID dari mode 1 pesan: data-mid="..."
	alightDataMidRe = regexp.MustCompile(`data-mid="([A-Za-z0-9_.-]+)"`)

	// ─── Anti-spam !bulk ──────────────────────────────────────────────────────
	bulkCooldownMu sync.Mutex
	bulkLastAt     = map[string]time.Time{}
	bulkBusyMu     sync.Mutex
	bulkBusy       bool

	// ─── !cancel ──────────────────────────────────────────────────────────────
	// Proses bulk yang berjalan bisa dibatalkan oleh yang MEMULAINYA atau owner.
	bulkCancelMu  sync.Mutex
	bulkCancelReq bool   // true = minta berhenti (dicek di polling loop)
	bulkStarter   string // nomor user yang memulai proses
)

// errBulkCanceled — penanda polling dihentikan karena !cancel (bukan error asli).
var errBulkCanceled = errors.New("proses dibatalkan")

// bulkCanceled cek apakah !cancel sudah diminta (dipanggil di polling loop).
func bulkCanceled() bool {
	bulkCancelMu.Lock()
	defer bulkCancelMu.Unlock()
	return bulkCancelReq
}

// bulkTryStart — cooldown per user untuk !bulk. Return (true, 0) kalau boleh
// lanjut (waktu langsung dicatat), (false, sisa) kalau masih kena cooldown.
// Map di-prune supaya tidak membengkak (pola sama dengan checkCooldown).
func bulkTryStart(user string) (bool, time.Duration) {
	bulkCooldownMu.Lock()
	defer bulkCooldownMu.Unlock()
	if t, ok := bulkLastAt[user]; ok {
		if elapsed := time.Since(t); elapsed < bulkCooldownTime {
			return false, bulkCooldownTime - elapsed
		}
	}
	if len(bulkLastAt) > 256 {
		for u, t := range bulkLastAt {
			if time.Since(t) > bulkCooldownTime {
				delete(bulkLastAt, u)
			}
		}
	}
	bulkLastAt[user] = time.Now()
	return true, 0
}

// ─── helper API & inbox ──────────────────────────────────────────────────────

// alightGenEmail — email sementara mtc.<8 char acak>@mtcommunty.web.id.
func alightGenEmail() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return "mtc." + string(b) + "@" + alightEmailDomain
}

// alightBulkID — ID proses bulk (mis. BULK-A1B2C3) supaya email/akun bisa
// dilacak kalau ada masalah — dipakai di pesan status WhatsApp.
func alightBulkID() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return "BULK-" + string(b)
}

// alightPost — POST JSON ke mtzhacie dengan cek status HTTP.
// Beda dari mtzPost: endpoint 4xx/5xx dianggap error (bukan "✅ palsu").
func alightPost(endpoint string, payload map[string]string) (string, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", mtzBaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", mtzAPIKey())

	resp, err := dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := readAllLimit(resp.Body, 2<<20)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return strings.TrimSpace(string(data)), nil
}

// alightExtractLink — persis extractOobLink() di generator.js asli:
// kumpulkan semua URL generik dari HTML, lalu pilih prioritas:
//  1. alight-creative.firebaseapp.com/__/auth/links
//  2. alight.link
//  3. oobCode / oob_code
func alightExtractLink(htmlBody string) string {
	urls := alightAnyURLRe.FindAllString(htmlBody, -1)
	first := func(pred func(string) bool) string {
		for _, u := range urls {
			u = html.UnescapeString(u)
			if pred(u) {
				return u
			}
		}
		return ""
	}
	if l := first(func(u string) bool { return alightFirebaseLinkRe.MatchString(u) }); l != "" {
		return l
	}
	if l := first(func(u string) bool { return alightLinkRe.MatchString(u) }); l != "" {
		return l
	}
	return first(func(u string) bool { return alightOobRe.MatchString(u) })
}

// alightFetchInboxPage — GET halaman inbox generator.email (format baru):
// path /inbox1/ saja + cookie `inbox_ctx=<domain>%2F<user>` (URL-encoded).
// Kalau msgID diisi → cookie jadi `domain%2Fuser%2FmsgID` → body pesan tsb
// dirender di halaman (bisa diextract link-nya).
func alightFetchInboxPage(user, msgID string) (string, error) {
	ctx := alightEmailDomain + "/" + user
	if msgID != "" {
		ctx += "/" + msgID
	}
	req, err := http.NewRequest("GET", alightInboxBase+"/inbox1/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", "inbox_ctx="+url.QueryEscape(ctx))
	resp, err := dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := readAllLimit(resp.Body, 8<<20)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// alightFetchInbox — GET halaman daftar inbox (tanpa msgID).
func alightFetchInbox(user string) (string, error) {
	return alightFetchInboxPage(user, "")
}

// alightFetchMessage — GET isi satu pesan inbox (cookie + msgID).
func alightFetchMessage(user, msgID string) (string, error) {
	return alightFetchInboxPage(user, msgID)
}

// alightWaitOobWS — WebSocket notificon generator.email (persis waitOobWs di
// generator.js asli): server kirim JSON {"link": "domain/user/MSGID"} realtime
// begitu email baru masuk. Channel mengirim msgID pertama lalu ditutup; kosong
// kalau timeout/koneksi putus — polling halaman tetap jadi fallback.
func alightWaitOobWS(user string, timeout time.Duration) <-chan string {
	ch := make(chan string, 1)
	wsURL := alightInboxBase + "/notificon/ws?email=" +
		url.QueryEscape(strings.ToLower(user+"@"+alightEmailDomain))
	go func() {
		defer close(ch)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Link string `json:"link"`
			}
			if json.Unmarshal(data, &msg) != nil || msg.Link == "" {
				continue
			}
			// "domain/user/MSGID" → msgID = bagian setelah 2 slash pertama.
			parts := strings.Split(msg.Link, "/")
			if len(parts) >= 3 {
				id := strings.Join(parts[2:], "/")
				select {
				case ch <- id:
				default:
				}
				return
			}
		}
	}()
	return ch
}

// alightCheckInbox — persis checkInbox() di generator.js asli: fetch halaman
// inbox (cookie + msgID kalau ada), parse num_mess, extract link dari halaman
// yang SAMA. num_mess 0 / fetch gagal → (0, "").
func alightCheckInbox(user, msgID string) (int, string) {
	html, err := alightFetchInboxPage(user, msgID)
	if err != nil {
		return 0, ""
	}
	count := 0
	if m := alightNumMessRe.FindStringSubmatch(html); len(m) >= 2 {
		count, _ = strconv.Atoi(m[1])
	}
	if count == 0 {
		return 0, ""
	}
	return count, alightExtractLink(html)
}

// alightListMsgIDs — kumpulkan ID pesan dari halaman inbox (data-mid pada mode
// 1 pesan, onclick loadInboxClientSide pada mode daftar 2+ pesan).
func alightListMsgIDs(pageHTML string) []string {
	seen := map[string]bool{}
	var ids []string
	for _, m := range alightDataMidRe.FindAllStringSubmatch(pageHTML, -1) {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	for _, m := range alightListMsgIDRe.FindAllStringSubmatch(pageHTML, -1) {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

// alightBaselineLink — link yang SUDAH ada di inbox sebelum user login
// (persis waitForLoginLink asli: baseline = link, bukan msgID). Link baru
// dikenali kalau BERBEDA dari baseline ini (atau datang dari WebSocket).
func alightBaselineLink(user string) string {
	_, link := alightCheckInbox(user, "")
	return link
}

// alightWaitInboxLink — tunggu magic link #1 masuk (persis generateAccount di
// generator.js asli): WebSocket notificon kasih msgID realtime → fetch body
// pesan itu langsung. Fallback: polling halaman (mode 1 pesan) / buka pesan
// daftar satu per satu — dipakai kalau WS mati.
func alightWaitInboxLink(user string) (string, error) {
	wsCh := alightWaitOobWS(user, alightWSTimeout)
	for i := 0; i < alightPollMax; i++ {
		// !cancel — polling berhenti, proses bulk dibatalkan.
		if bulkCanceled() {
			return "", errBulkCanceled
		}
		var msgID string
		select {
		case id, ok := <-wsCh:
			if ok {
				msgID = id
			}
		default:
		}
		if _, link := alightCheckInbox(user, msgID); link != "" {
			return link, nil
		}
		// Fallback mode daftar (2+ pesan & WS tidak dapat msgID): buka pesan
		// satu per satu sampai ketemu link.
		if msgID == "" {
			if html, err := alightFetchInbox(user); err == nil {
				for _, id := range alightListMsgIDs(html) {
					msg, err := alightFetchMessage(user, id)
					if err != nil {
						continue
					}
					if link := alightExtractLink(msg); link != "" {
						return link, nil
					}
				}
			}
		}
		time.Sleep(alightPollDelay)
	}
	return "", fmt.Errorf("magic link tidak muncul di inbox setelah %d×%ds", alightPollMax, int(alightPollDelay.Seconds()))
}

// alightWaitNewInboxLink — persis waitForLoginLink() di generator.js asli:
// tunggu link BARU yang dikirim Firebase setelah user login.
//
//   - WebSocket kasih msgID → pesan itu PASTI baru (langsung terima).
//   - Tanpa WS: link diterima kalau BERBEDA dari baselineLink (anti link lama).
//
// NOTE: halaman inbox mode daftar (2+ pesan) tidak memuat body, jadi tanpa
// msgID dari WS link baru tidak akan kelihatan — WS adalah jalur utamanya.
func alightWaitNewInboxLink(user, baselineLink string, maxAttempts int, delay time.Duration) (string, error) {
	wsCh := alightWaitOobWS(user, time.Duration(maxAttempts)*delay)
	deadline := time.Now().Add(time.Duration(maxAttempts) * delay)
	for time.Now().Before(deadline) {
		// !cancel — polling berhenti, proses bulk dibatalkan.
		if bulkCanceled() {
			return "", errBulkCanceled
		}
		var msgID string
		select {
		case id, ok := <-wsCh:
			if ok {
				msgID = id
			}
		default:
		}
		if _, link := alightCheckInbox(user, msgID); link != "" && (msgID != "" || link != baselineLink) {
			return link, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("magic link tidak masuk ke inbox setelah %d menit", maxAttempts*int(delay)/60)
}

// alightGenerateAccount — TAHAP 1: satu siklus akun — gen email → send-link →
// tunggu magic link #1 → aktifasi (premium). Return email (akun SUDAH aktif).
func alightGenerateAccount() (string, error) {
	if mtzAPIKey() == "" {
		return "", fmt.Errorf("MTZAPIKey belum diatur di config.go (atau env `MTZ_API_KEY`)")
	}

	email := alightGenEmail()
	user := strings.SplitN(email, "@", 2)[0]

	// 1. kirim magic link #1 ke email
	if _, err := alightPost(mtzSendLink, map[string]string{"email": email}); err != nil {
		return "", fmt.Errorf("send-link: %w", err)
	}
	// 2. tunggu link #1 di inbox
	link1, err := alightWaitInboxLink(user)
	if err != nil {
		return "", err
	}
	// 3. aktifkan akun (server yang sign-in + verifyPurchase + tulis license).
	//    Link #1 sudah terpakai server — link Firebase sekali pakai.
	if _, err := alightPost(mtzAktifasi, map[string]string{"email": email, "link": link1}); err != nil {
		return "", fmt.Errorf("aktifasi: %w", err)
	}
	return email, nil
}

// ─── !bulk <jumlah> (max 1) ─────────────────────────────────────────────────

func handleBulkGenerate(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat

	// Anti-spam 1: cooldown per user (2 menit).
	if ok, wait := bulkTryStart(senderUser(evt)); !ok {
		sendText(ctx, chat, fmt.Sprintf("⏳ Cooldown, tunggu %ds lagi.", int(wait.Seconds())+1))
		return
	}
	// Anti-spam 2: hanya 1 proses bulk yang boleh berjalan global — user lain
	// yang spam akan ditolak sampai proses selesai.
	bulkBusyMu.Lock()
	if bulkBusy {
		bulkBusyMu.Unlock()
		sendText(ctx, chat, "⏳ Masih ada proses *Bulk Alight Motion* berjalan. Tunggu selesai dulu ya.")
		return
	}
	bulkBusy = true
	bulkBusyMu.Unlock()
	// Catat siapa yang memulai proses — hanya dia (atau owner) yang bisa !cancel.
	bulkCancelMu.Lock()
	bulkStarter = senderUser(evt)
	bulkCancelReq = false
	bulkCancelMu.Unlock()
	defer func() {
		bulkBusyMu.Lock()
		bulkBusy = false
		bulkBusyMu.Unlock()
		bulkCancelMu.Lock()
		bulkStarter = ""
		bulkCancelMu.Unlock()
	}()

	n := 1
	if v := strings.TrimSpace(args); v != "" {
		parsed, err := strconv.Atoi(strings.Fields(v)[0])
		if err != nil || parsed < 1 {
			sendText(ctx, chat, fmt.Sprintf(
				"🎞️ *Bulk Alight Motion*\n\n"+
					"> Bot buat akun Alight Motion premium otomatis\n"+
					"> (email sementara + aktivasi via magic link)\n\n"+
					"*Format:*\n"+
					"> `%sbulk 1`\n\n"+
					"*Catatan:* maksimal 1 akun per command.",
				Prefix))
			return
		}
		n = parsed
	}
	if n > 1 {
		n = 1 // clamp: max 1 akun per command
	}

	bulkID := alightBulkID()

	reactMsg(ctx, evt, "🎞️")
	sendText(ctx, chat, fmt.Sprintf("⏳ Membuat %d akun Alight Motion premium... (±1 menit)\nID proses: `%s`", n, bulkID))

	var hasil []string
	for i := 0; i < n; i++ {
		// !cancel — hentikan sebelum iterasi berikutnya.
		if bulkCanceled() {
			reactMsg(ctx, evt, "🛑")
			sendText(ctx, chat, "🛑 *Bulk Alight Motion dibatalkan* (ID proses: `"+bulkID+"`).")
			return
		}
		// TAHAP 1: aktifasi — email dikirim, akun SUDAH aktif & premium.
		email, err := alightGenerateAccount()
		if err != nil {
			if errors.Is(err, errBulkCanceled) {
				reactMsg(ctx, evt, "🛑")
				sendText(ctx, chat, "🛑 *Bulk Alight Motion dibatalkan* (ID proses: `"+bulkID+"`).")
				return
			}
			hasil = append(hasil, fmt.Sprintf("• ❌ Akun %d gagal: %s", i+1, err))
			continue
		}
		sendText(ctx, chat, fmt.Sprintf(
			"🎞️ *Bulk Alight Motion — Akun Siap*\n\n"+
				"> ID: `%s`\n"+
				"> Email: `%s`\n\n"+
				"✅ Akun sudah aktif & premium otomatis.\n\n"+
				"*Langkah berikutnya:*\n"+
				"1. Buka aplikasi/situs Alight Motion\n"+
				"2. Login pakai email di atas\n"+
				"3. Magic link baru akan masuk ke email itu\n\n"+
				"⏳ Bot menunggu magic link (±5 menit)...",
			bulkID, email))

		// TAHAP 2: tunggu link FRESH yang dikirim Firebase setelah user login.
		user := strings.SplitN(email, "@", 2)[0]
		baselineLink := alightBaselineLink(user)
		loginLink, err := alightWaitNewInboxLink(user, baselineLink, alightBulkPollMax, alightBulkPollDelay)
		if err != nil {
			if errors.Is(err, errBulkCanceled) {
				reactMsg(ctx, evt, "🛑")
				sendText(ctx, chat, "🛑 *Bulk Alight Motion dibatalkan* (ID proses: `"+bulkID+"`).")
				return
			}
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ *Bulk Alight Motion*\n\n"+err.Error()+
				"\n\nID proses: `"+bulkID+"`\nEmail: `"+email+"`")
			continue
		}
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "🎞️ *Bulk Alight Motion — Magic Link*\n\n"+
			"> ID: `"+bulkID+"`\n"+
			"> Email: `"+email+"`\n"+
			"> Link: "+loginLink+"\n\n"+
			"✅ Buka link untuk masuk ke akun Alight Motion premium.")
	}
}

// ─── !cancel ─────────────────────────────────────────────────────────────────
// Batalkan proses Bulk Alight Motion yang sedang berjalan. Hanya yang MEMULAI
// proses (bulkStarter) atau owner yang boleh; user lain ditolak.

func handleBulkCancel(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	user := senderUser(evt)

	bulkBusyMu.Lock()
	busy := bulkBusy
	bulkBusyMu.Unlock()
	if !busy {
		sendText(ctx, chat, "📭 Tidak ada proses *Bulk Alight Motion* yang sedang berjalan.")
		return
	}

	bulkCancelMu.Lock()
	starter := bulkStarter
	bulkCancelMu.Unlock()
	if user != starter && !isCreator(user) && !isOwnerDB(user) {
		sendText(ctx, chat, "❌ Hanya yang memulai proses (`"+starter+"`) atau *owner* yang bisa membatalkan.")
		return
	}

	bulkCancelMu.Lock()
	bulkCancelReq = true
	bulkCancelMu.Unlock()
	reactMsg(ctx, evt, "🛑")
	sendText(ctx, chat, "🛑 Proses *Bulk Alight Motion* dibatalkan... (berhenti di langkah berikutnya)")
}
