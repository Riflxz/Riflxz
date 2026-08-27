package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Queue & Session ─────────────────────────────────────────────────────────

type queueItem struct {
	track *spotifyTrack
	file  string
	evt   *events.Message
}

type callSession struct {
	mu        sync.Mutex
	sender    *Sender // sender yang naruh call ini
	active    bool
	target    string
	shortID   string
	requester string // user yang manggil .playcall — dipake buat validasi tap tombol
	queue     []queueItem
	player    *meowcaller.Player
	call      *meowcaller.Call
	stopHB    chan struct{}
	done      chan struct{}
	once      sync.Once
	// cleanupOnce memastikan cleanup (reset + snd.sess=nil + stopAll) hanya
	// dijalankan SEKALI meski OnEnd dan goroutine timeout berjalan bersamaan.
	cleanupOnce sync.Once
	playNext    func() // dipanggil manual saat skip
	// playMu: serialisasi playNextFn. Tanpa ini, dua goroutine (OnFinish, skip,
	// OnReady, addSong, jalur error) bisa pop antrian & Play bersamaan — lagu
	// ke-skip tanpa sebab dan file-nya bocor (curFile saling menimpa).
	playMu sync.Mutex

	// nowTitle: judul lagu yang sedang diputar saat ini (dipakai handleAntri).
	// Sebelumnya bernama prankTitle — dinomori ulang agar tidak membingungkan.
	pranking  bool
	curFile   string // path mp3 lagu yang lagi diputer (buat resume abis prank)
	prankFile string // path mp3 temp prank — di-cleanup setelah OnFinish prank
	nowTitle  string // judul lagu yang sedang diputar (dulunya prankTitle)
}

func (s *callSession) reset() {
	s.active = false
	s.target = ""
	s.shortID = ""
	s.queue = nil
	s.player = nil
	s.call = nil
	s.playNext = nil
	s.pranking = false
	s.curFile = ""
	s.prankFile = ""
	s.nowTitle = ""
}

// doCleanup: cleanup terpusat yang dijamin hanya jalan SEKALI via cleanupOnce.
// Fix RC-02: OnEnd dan goroutine timeout tidak lagi bisa double-cleanup.
func (s *callSession) doCleanup(snd *Sender) {
	s.cleanupOnce.Do(func() {
		s.mu.Lock()
		for _, q := range s.queue {
			os.Remove(q.file)
		}
		if s.curFile != "" {
			os.Remove(s.curFile)
		}
		// Fix: prankFile juga harus dibersihkan kalau call mati di tengah prank.
		if s.prankFile != "" {
			os.Remove(s.prankFile)
		}
		s.reset()
		s.mu.Unlock()
		snd.mu.Lock()
		snd.sess = nil
		snd.mu.Unlock()
		s.stopAll()
		// Fix: audio call selesai = sender nganggur — antrian .playvideo yang
		// nunggu sender harus dicoba lagi (sebelumnya advanceVideoQueue hanya
		// dipanggil dari endSession video, jadi video yang antri di belakang
		// audio call tak pernah jalan).
		go advanceVideoQueue()
	})
}

func (s *callSession) stopAll() {
	s.once.Do(func() {
		select {
		case <-s.stopHB:
		default:
			close(s.stopHB)
		}
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	})
}

// ─── Mode ─────────────────────────────────────────────────────────────────────

var (
	modeMu sync.RWMutex
	mode   = "self"
)

func getMode() string  { modeMu.RLock(); defer modeMu.RUnlock(); return mode }
func setMode(m string) { modeMu.Lock(); mode = m; modeMu.Unlock() }

// ─── Mode ONLYGRUP (!kacang) ────────────────────────────────────────────────
// Saat ON, semua PM (chat 1:1) dari non-owner diabaikan total — hanya owner
// yang direspon di PM. Grup, status, dan newsletter tidak terpengaruh.

var (
	onlyGrupMu sync.RWMutex
	onlyGrup   bool
)

func getOnlyGrup() bool  { onlyGrupMu.RLock(); defer onlyGrupMu.RUnlock(); return onlyGrup }
func setOnlyGrup(b bool) { onlyGrupMu.Lock(); onlyGrup = b; onlyGrupMu.Unlock() }

// ─── Cooldown ───────────────────────────────────────────────────────────────
// Dipake bareng buat .playcall & .playvideo, per pengirim. Sebelumnya
// PlaycallCooldownSeconds cuma didefinisiin tapi gak pernah di-enforce — ini
// benerin itu.

var (
	cooldownMu sync.Mutex
	lastCallAt = map[string]time.Time{}
)

// checkCooldown balikin (true, 0) kalau boleh lanjut (dan langsung nyatet
// waktunya), atau (false, sisaWaktu) kalau masih kena cooldown.
// Fix: map lastCallAt di-prune supaya tidak membengkak tanpa batas (memory leak).
func checkCooldown(user string) (bool, time.Duration) {
	cooldownMu.Lock()
	defer cooldownMu.Unlock()
	limit := time.Duration(PlaycallCooldownSeconds) * time.Second
	if t, ok := lastCallAt[user]; ok {
		if elapsed := time.Since(t); elapsed < limit {
			return false, limit - elapsed
		}
	}
	if len(lastCallAt) > 256 {
		for u, t := range lastCallAt {
			if time.Since(t) > limit {
				delete(lastCallAt, u)
			}
		}
	}
	lastCallAt[user] = time.Now()
	return true, 0
}

// senderUser ngambil nomor/user id pengirim dari sebuah events.Message, konsisten
// sama logika resolusi sender di handleMessage (pake SenderAlt kalau ada).
// senderUser return nomor pengirim sebagai string (tanpa "@...").
// Fix LID bug: di grup, sender bisa dalam format @lid (privacy ID WhatsApp)
// bukan nomor asli. Kalau keduanya LID, coba resolve ke nomor via LID store.
func senderUser(evt *events.Message) string {
	sender := evt.Info.Sender.ToNonAD()
	alt := evt.Info.MessageSource.SenderAlt

	if !alt.IsEmpty() {
		altND := alt.ToNonAD()
		// Pilih yang BUKAN LID — itu nomor asli
		if altND.Server != types.HiddenUserServer {
			return altND.User
		}
		// Alt juga LID: kalau sender bukan LID, tetap pakai sender
		if sender.Server != types.HiddenUserServer {
			return sender.User
		}
		// Keduanya LID, pakai alt untuk resolusi
		sender = altND
	}

	// Kalau sender LID, coba resolve ke nomor asli via LID store
	if sender.Server == types.HiddenUserServer {
		if pn, err := waClient.Store.LIDs.GetPNForLID(context.Background(), sender); err == nil && !pn.IsEmpty() && pn.User != "" {
			return pn.User
		}
	}

	return sender.User
}

// cleanJIDNumber motong suffix "@server" dan ":device" dari sebuah JID/nomor
// string, nyisain nomor polos doang. Dipake buat nampilin target di pesan.
func cleanJIDNumber(target string) string {
	return strings.Split(strings.Split(target, "@")[0], ":")[0]
}

// ─── Router ──────────────────────────────────────────────────────────────────

func handleMessage(ctx context.Context, evt *events.Message) {
	// Abaikan pesan dari newsletter/channel — tidak relevan buat bot.
	if evt.Info.Chat.Server == "newsletter" {
		return
	}

	// Mode ONLYGRUP (!kacang on): abaikan semua PM (chat 1:1 "s.whatsapp.net"
	// atau "lid") dari non-owner — owner tetap dilayani. Status broadcast
	// ("broadcast") & grup ("g.us") tidak terpengaruh. Ditaruh sebelum parsing
	// supaya PM non-owner tidak direspon sama sekali (command maupun non-command).
	if getOnlyGrup() && evt.Info.Chat.Server != "g.us" && evt.Info.Chat.Server != "broadcast" &&
		!evt.Info.IsFromMe && !isOwnerDB(senderUser(evt)) {
		return
	}

	// Ekstrak teks dari semua tipe pesan yang mungkin
	text := evt.Message.GetConversation()
	if text == "" {
		text = evt.Message.GetExtendedTextMessage().GetText()
	}
	if text == "" {
		text = evt.Message.GetImageMessage().GetCaption()
	}
	if text == "" {
		text = evt.Message.GetVideoMessage().GetCaption()
	}
	text = strings.TrimSpace(text)

	// Cek button click dari interactive message — ekstrak ID tombol sebagai command
	if text == "" {
		if btnCmd := extractButtonCommand(evt); btnCmd != "" {
			text = btnCmd
		}
	}

	// Fitur jaga grup: moderasi pesan (antilink/antitoxic) — jalan untuk
	// SEMUA pesan grup, termasuk yang bukan command. Harus sebelum guard
	// mode & dispatch supaya tidak bisa dimatikan lewat perintah.
	scanGroupGuard(ctx, evt, text)

	// Fitur AFK: cek apakah sender baru kembali dari AFK (hapus status &
	// beri tahu), dan cek apakah ada mention ke user yang sedang AFK.
	// Jalan untuk SEMUA pesan (command maupun non-command).
	checkAfkReturn(ctx, evt)
	checkAfkMention(ctx, evt)

	// Tandai ctx dengan evt command — semua sendText/media di bawah ini
	// otomatis jadi REPLY ke pesan perintah user (bukan pesan terpisah).
	ctx = withCommandEvt(ctx, evt)

	// Handle howto: prefix — command yang butuh input manual.
	// Diklik dari menu V3 → tampilkan panduan cara pakai, bukan error.
	if strings.HasPrefix(text, howtoPrefix) {
		// Fix SELF-HOWTO: blok ini dulu dieksekusi SEBELUM guard mode di bawah,
		// jadi di self mode non-owner tetap bisa menarik panduan (termasuk
		// sintaks command owner seperti *!shell*) — bocoran & disobey mode.
		// Terapkan guard yang sama dengan command lain.
		user := senderUser(evt)
		ownerSender := isOwner(user) || isOwnerDB(user)
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		cmdKey := strings.TrimPrefix(text, howtoPrefix)
		if guide, ok := howtoText[cmdKey]; ok {
			sendText(ctx, evt.Info.Chat, guide)
		} else {
			sendText(ctx, evt.Info.Chat, fmt.Sprintf(
				"ℹ️ Ketik manual: *%s%s*", Prefix, cmdKey))
		}
		return
	}

	if evt.Info.IsFromMe || text == "" || !strings.HasPrefix(text, Prefix) {
		// Fitur AI chat: pesan non-command yang diawali "yuuki"/"yuki"
		// (mis. "yuuki apa kabar") diteruskan ke AI — berlaku untuk SEMUA
		// user, selama AI aktif (!ai on). Blacklist tetap dihormati.
		if !evt.Info.IsFromMe && text != "" && aiChatEnabled() && !isBlocked(evt) {
			if prompt, ok := aiExtractPrompt(text); ok {
				safeGo(func() { handleAiChat(ctx, evt, prompt) })
				return
			}
		}
		return
	}

	// Blacklist: sender atau grup tempat command dijalankan masuk daftar hitam
	// → semua command ditolak (owner selalu lolos).
	if isBlocked(evt) {
		return
	}

	body := strings.TrimSpace(strings.TrimPrefix(text, Prefix))
	if body == "" {
		return
	}

	fields := strings.Fields(body)
	cmd := strings.ToLower(fields[0])
	args := strings.TrimSpace(strings.TrimPrefix(body, fields[0]))

	user := senderUser(evt)

	// isOwnerDB sudah include isCreator — pakai ini sebagai sumber kebenaran tunggal.
	// ownerSender juga di-OR dengan ownerLevel supaya LID resolution failure
	// tidak memblokir owner yang sudah teridentifikasi via DB.
	ownerLevel := isOwnerDB(user)
	creatorLevel := isCreator(user)
	ownerSender := isOwner(user) || ownerLevel
	premiumSender := isPremiumDB(user)

	switch cmd {

	// ── Umum — selalu respon tanpa cek mode/owner ─────────────────────────────
	case "menu", "help", "h":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		sendMenu(ctx, evt.Info.Chat, evt, ownerSender, ownerLevel, premiumSender)

	case "allmenu", "all":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		sendText(ctx, evt.Info.Chat, allMenuText(ownerSender, ownerLevel, premiumSender))

	case "menucat", "mc", "category":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleMenuCat(ctx, evt, args, ownerSender, ownerLevel, premiumSender) })

	case "ping", "p":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handlePing(ctx, evt)

	case "donasi", "donate", "donation", "qris":
		// Fix: donasi dibuka untuk SEMUA user, apa pun mode bot (sebelumnya
		// requirePublicOrOwner → di mode self hanya owner yang bisa).
		safeGo(func() { handleDonasi(ctx, evt) })

	case "contributor", "tqto", "thanks":
		// Terbuka untuk semua user — daftar kontributor bot.
		sendText(ctx, evt.Info.Chat, contributorText())

	case "afk":
		// Tandai user sedang AFK — terbuka untuk semua user.
		safeGo(func() { handleAfk(ctx, evt, args) })

	case "amkey", "am-key":
		// Set API key Alight Motion — terbuka untuk pembeli key (10k/bulan).
		safeGo(func() { handleAMKey(ctx, evt, args) })

	case "info":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handleInfo(ctx, evt)

	case "myjid", "jid":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handleMyJID(ctx, evt)

	case "owner", "cekowner", "whoami":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handleOwnerCheck(ctx, evt)

	case "sticker", "s", "stk":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleSticker(ctx, evt) })

	case "sai", "stickerai":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleSai(ctx, evt, args) })

	// ── Mode ─────────────────────────────────────────────────────────────────

	case "self":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		setMode("self")
		sendText(ctx, evt.Info.Chat, "🔒 Mode *SELF* — bot cuma respon owner.")

	case "public":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		setMode("public")
		sendText(ctx, evt.Info.Chat, "🌐 Mode *PUBLIC* — command umum bisa dipake semua.")

	case "kacang", "onlygrup":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		switch strings.ToLower(strings.TrimSpace(args)) {
		case "on", "nyala":
			setOnlyGrup(true)
			sendText(ctx, evt.Info.Chat,
				"🌰 Mode *ONLYGRUP*: ON — PM dari non-owner diabaikan,\n"+
					"hanya owner yang direspon.")
		case "off", "mati":
			setOnlyGrup(false)
			sendText(ctx, evt.Info.Chat,
				"🌰 Mode *ONLYGRUP*: OFF — semua PM dilayani normal.")
		default:
			status := "OFF"
			if getOnlyGrup() {
				status = "ON"
			}
			sendText(ctx, evt.Info.Chat, fmt.Sprintf(
				"🌰 Mode *ONLYGRUP*: %s\n\n"+
					"Gunakan: *%skacang on* / *%skacang off*",
				status, Prefix, Prefix))
		}

	// ── Audio Call ────────────────────────────────────────────────────────────

	case "play", "playcall", "pc":
		if !requireOwnerOrPremium(ctx, evt, ownerSender, user) {
			return
		}
		safeGo(func() { handlePlaycall(ctx, evt, args) })

	case "skip", "sk":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleSkip(ctx, evt, args)

	case "stop", "stopcall", "sc":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleStop(ctx, evt, args)

	case "continue", "cnt", "lanjut":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		fields := strings.Fields(args)
		if len(fields) == 0 {
			handleContinue(ctx, evt, "")
		} else {
			last := strings.ToLower(fields[len(fields)-1])
			active := pool.activeSessions()
			senderArg, titleArg := "", args
			if strings.HasPrefix(last, "sender") && pool.byName(last) != nil {
				senderArg = last
				titleArg = strings.TrimSpace(strings.Join(fields[:len(fields)-1], " "))
			}
			if titleArg == "" {
				handleContinue(ctx, evt, senderArg)
			} else {
				var targetSess *callSession
				if senderArg != "" {
					if s := pool.byName(senderArg); s != nil {
						s.mu.Lock()
						targetSess = s.sess
						s.mu.Unlock()
					}
				} else if len(active) == 1 {
					targetSess = active[0]
				} else if len(active) > 1 {
					sendText(ctx, evt.Info.Chat, fmt.Sprintf(
						"❓ Ada %d call aktif. Sebutkan sender:\n*%slanjut <judul> sender1*", len(active), Prefix))
					return
				}
				if targetSess == nil {
					sendText(ctx, evt.Info.Chat, "📭 Tidak ada call aktif.")
					return
				}
				safeGo(func() { addSongToSession(ctx, evt, targetSess, titleArg) })
			}
		}

	case "queue", "antrian", "q":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handleQueue(ctx, evt, args, ownerSender)

	case "antri", "cekantri":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleAntri(ctx, evt, args)

	case "hapus", "del":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleHapus(ctx, evt, args)

	case "prank":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handlePrank(ctx, evt, args) })

	// ── Video Call ────────────────────────────────────────────────────────────

	case "video", "playvideo", "pv":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handlePlayVideo(ctx, evt, args) })

	case "stopvideo", "sv":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleStopVideo(ctx, evt, args)

	case "skipvideo", "skv":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleSkipVideo(ctx, evt, args)

	case "antrianvideo", "qv":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		handleVideoQueue(ctx, evt, ownerSender)

	// ── Sender ────────────────────────────────────────────────────────────────

	case "addsender", "adds":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleAddSender(ctx, evt, args) })

	case "canceladd", "cancels":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		if err := pool.removeSender(strings.TrimSpace(args)); err != nil {
			sendText(ctx, evt.Info.Chat, "❌ "+err.Error())
			return
		}
		reactMsg(ctx, evt, "🗑️")
		sendText(ctx, evt.Info.Chat, "🗑️ Pairing "+strings.TrimSpace(args)+" dibatalin.")

	case "listsender", "senders", "ls":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleListSender(ctx, evt)

	// ── Tools ─────────────────────────────────────────────────────────────────

	case "bypass", "bypasslink":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleBypassLink(ctx, evt, args) })

	case "bl", "blacklist":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleBlacklist(ctx, evt, args) })

	case "clear", "clearcache":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleClearCache(ctx, evt) })

	case "gstatus", "swgc2":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleSWGC2(ctx, evt, args) })

	case "jaga", "guard":
		safeGo(func() { handleGuardCmd(ctx, evt, args, "") })

	case "antilink":
		safeGo(func() { handleGuardCmd(ctx, evt, args, "antilink") })

	case "antitoxic":
		safeGo(func() { handleGuardCmd(ctx, evt, args, "antitoxic") })

	case "welcome":
		safeGo(func() { handleGuardCmd(ctx, evt, args, "welcome") })

	case "playch":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handlePlayCh(ctx, evt, args) })

	case "close", "kunci":
		safeGo(func() { handleGroupClose(ctx, evt, args) })

	case "open", "buka":
		safeGo(func() { handleGroupOpen(ctx, evt, args) })

	case "kick", "tendang":
		safeGo(func() { handleGroupKick(ctx, evt, args) })

	case "out", "keluar":
		safeGo(func() { handleGroupOut(ctx, evt, args) })

	case "add", "tambahangota":
		safeGo(func() { handleGroupAdd(ctx, evt, args) })

	case "promote", "naikadmin":
		safeGo(func() { handleGroupPromote(ctx, evt, args, false) })

	case "demote", "turunadmin":
		safeGo(func() { handleGroupPromote(ctx, evt, args, true) })

	case "tagall", "tag", "tall":
		safeGo(func() { handleTagAll(ctx, evt, args) })

	case "hidetag", "htag":
		safeGo(func() { handleHideTag(ctx, evt, args) })

	case "setname", "setsubject":
		safeGo(func() { handleGroupSetName(ctx, evt, args) })

	case "setdesc", "setdesk":
		safeGo(func() { handleGroupSetDesc(ctx, evt, args) })

	case "setppgc", "setpp":
		safeGo(func() { handleGroupSetPP(ctx, evt, args) })

	case "warn":
		safeGo(func() { handleWarn(ctx, evt, args) })

	case "resetwarn", "hapuswarn":
		safeGo(func() { handleResetWarn(ctx, evt, args) })

	case "warnlist", "listwarn":
		safeGo(func() { handleWarnList(ctx, evt, args) })

	case "linkgc", "linkgrup":
		safeGo(func() { handleGroupLink(ctx, evt, args) })

	case "revoke", "resetlink":
		safeGo(func() { handleGroupRevoke(ctx, evt, args) })

	case "infogc", "groupinfo", "infogrup":
		safeGo(func() { handleGroupInfo(ctx, evt, args) })

	case "setwelcome":
		safeGo(func() { handleSetWelcome(ctx, evt, args) })

	// ── JPM Broadcast (khusus owner) ──────────────────────────────────────────

	case "jpm", "jasher", "jaser":
		safeGo(func() { handleJpm(ctx, evt, args, "jpm") })

	case "jpmht", "jpmhidetag":
		safeGo(func() { handleJpm(ctx, evt, args, "jpmht") })

	case "jpmch", "jpmchannel":
		safeGo(func() { handleJpm(ctx, evt, args, "jpmch") })

	case "autojpm", "autojasher":
		safeGo(func() { handleJpm(ctx, evt, args, "autojpm") })

	case "stopjpm", "stopjasher":
		safeGo(func() { handleJpm(ctx, evt, args, "stopjpm") })

	case "setdelayjpm", "delayjpm", "jedajpm", "setjedajpm":
		safeGo(func() { handleJpm(ctx, evt, args, "setdelayjpm") })

	case "bljpm", "jpmbl", "jpmblacklist", "blacklistjpm":
		safeGo(func() { handleJpm(ctx, evt, args, "bljpm") })

	case "blautojpm", "autojpmbl", "blacklistautojpm":
		safeGo(func() { handleJpm(ctx, evt, args, "blautojpm") })

	case "jpmupdate", "updatejpm", "broadcastupdate":
		safeGo(func() { handleJpm(ctx, evt, args, "jpmupdate") })

	case "saluran", "listsaluran":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleListSaluran(ctx, evt) })

	case "kirim", "kirimch":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleKirimCh(ctx, evt, args) })

	case "upch", "sendch":
		// Fix: dulu di-router ke handleKirimCh (picker saluran) — sekarang
		// ke handleUpCh (posting langsung ke SwGC2JID, sesuai konstanta &
		// panduan menu). Guard owner: handleUpCh sendiri tidak punya guard.
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleUpCh(ctx, evt) })

	// case "upswch", "setstatusch":
	// 	// ⚠️ DISABLED sementara — metode status channel belum ketemu (riset
	// 	// protokol & mutation mentok). Aktifkan lagi kalau sudah ada jalurnya.
	// 	if !requireOwner(ctx, evt, ownerSender) {
	// 		return
	// 	}
	// 	safeGo(func() { handleUpSwCh(ctx, evt, args) })

	case "fetchch", "fetchchannel":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleFetchCh(ctx, evt, args) })

	// ── Owner/Creator ─────────────────────────────────────────────────────────

	case "addowner", "ao":
		if !creatorLevel {
			if getMode() == "public" {
				sendText(ctx, evt.Info.Chat, "🔒 Khusus *creator* bot.")
			}
			return
		}
		handleAddOwner(ctx, evt, args)

	case "delowner", "do":
		if !creatorLevel {
			if getMode() == "public" {
				sendText(ctx, evt.Info.Chat, "🔒 Khusus *creator* bot.")
			}
			return
		}
		handleDelOwner(ctx, evt, args)

	case "addprem", "ap":
		if !ownerLevel {
			if getMode() == "public" {
				sendText(ctx, evt.Info.Chat, "🔒 Khusus *owner* bot.")
			}
			return
		}
		handleAddPrem(ctx, evt, args)

	case "delprem", "dp":
		if !ownerLevel {
			if getMode() == "public" {
				sendText(ctx, evt.Info.Chat, "🔒 Khusus *owner* bot.")
			}
			return
		}
		handleDelPrem(ctx, evt, args)

	case "shell", "exec", "sh":
		if !creatorLevel {
			if getMode() == "public" {
				sendText(ctx, evt.Info.Chat, "🔒 Khusus *creator* bot.")
			}
			return
		}
		safeGo(func() { handleShell(ctx, evt, args) })

	case "setmenu":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		handleSetMenu(ctx, evt, args)

	case "read", "autoread":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleRead(ctx, evt, args) })

	// ── Downloader ────────────────────────────────────────────────────────────

	case "tt", "tiktok", "ttdl":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTikTok(ctx, evt, args) })

	case "tthd", "tiktokhd":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTikTokHD(ctx, evt, args) })

	case "ytmp3", "yta":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleYTMP3(ctx, evt, args) })

	case "ig", "igdl", "instagram":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleInstagram(ctx, evt, args) })

	case "fb", "fbdl", "facebook":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleFacebook(ctx, evt, args) })

	case "soundcloud", "scmusic":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleSoundCloud(ctx, evt, args) })

	// ── Tools ─────────────────────────────────────────────────────────────────

	case "tr", "translate", "tran":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTranslate(ctx, evt, args) })

	case "qr", "qrcode":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleQRCode(ctx, evt, args) })

	case "ss", "ssweb", "webss":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleScreenshot(ctx, evt, args) })

	case "tomp3", "mp3", "toaudio":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleToMP3(ctx, evt) })

	case "toimg", "stk2img":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleToImg(ctx, evt) })

	case "togif", "tomp4", "stk2mp4":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleToGIF(ctx, evt) })

	case "tourl", "tolink", "upload":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleToURL(ctx, evt) })

	case "barcode":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBarcode(ctx, evt, args) })

	case "blur":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBlur(ctx, evt) })

	case "cap", "caption":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCaption(ctx, evt, args) })

	case "compress", "kompres":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCompress(ctx, evt, args) })

	case "hd", "hdr":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleHD(ctx, evt) })

	// case "hdvid", "hdvideo", "vhd", "enhancevid", "hdv":
	// 	// ⚠️ DISABLED sementara — pakai ffmpeg yang berat (upscale video).
	// 	// Aktifkan lagi kalau sudah ada jalur yang lebih ringan.
	// 	if !requirePublicOrOwner(ownerSender) {
	// 		return
	// 	}
	// 	safeGo(func() { handleHDVid(ctx, evt) })

	case "removebg", "rbg":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleRemoveBG(ctx, evt) })

	case "tofile":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleToFile(ctx, evt, args) })

	case "uploadgh", "ghupload", "tourlgh":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleUpGH(ctx, evt, args) })

	case "resend":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleResend(ctx, evt, args) })

	case "rch", "reactch":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleReactChannel(ctx, evt, args) })

	case "am-send", "amsend", "sendlink":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleAMSend(ctx, evt, args) })

	case "am-aktif", "amaktif", "aktifasi":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleAMAktif(ctx, evt, args) })

	case "bulk", "bulkalight":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBulkGenerate(ctx, evt, args) })

	case "cancel":
		safeGo(func() { handleBulkCancel(ctx, evt) })

	case "fetch", "geturl":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleFetch(ctx, evt, args) })

	case "tempmail", "tmail", "cekmail":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTempMail(ctx, evt, args) })

	case "rvo", "readviewonce":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleReadViewOnce(ctx, evt) })

	case "delmsg":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleDeleteMsg(ctx, evt) })

	case "whatmusic", "wmusic", "whatmusik":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleWhatMusic(ctx, evt) })

	case "source", "websource":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleSource(ctx, evt, args) })

	case "idch", "cekidch":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekIDCh(ctx, evt, args) })

	case "getidch":
		// Bisa untuk semua user (tanpa gate mode) — lookup read-only.
		safeGo(func() { handleCekIDCh(ctx, evt, args) })

	case "idgc", "cekidgc":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekIDGC(ctx, evt, args) })

	case "kodebahasa", "kodebhs":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleKodeBahasa(ctx, evt) })

	case "enc", "encode":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleEnc(ctx, evt, args) })

	case "wanted":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleWanted(ctx, evt) })

	// ── Search ───────────────────────────────────────────────────────────────

	case "wiki", "wikipedia", "wikiid":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleWikipedia(ctx, evt, args) })

	case "kbbi":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleKBBI(ctx, evt, args) })

	case "lyrics", "lirik":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleLyrics(ctx, evt, args) })

	case "codesnap":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCodeSnap(ctx, evt, args) })

	case "douyin":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleDouyin(ctx, evt, args) })

	case "applemusic", "amusic":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleAppleMusic(ctx, evt, args) })

	case "yts", "youtube", "youtubesearch":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleYTSearch(ctx, evt, args) })

	case "ytplay", "ytp":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleYTPlay(ctx, evt, args) })

	case "bingimage", "bingimg":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBingImage(ctx, evt, args) })

	case "pinterest", "pin":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handlePinterest(ctx, evt, args) })

	case "pinpick", "pinpilih":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handlePinPick(ctx, evt, args) })

	// ── Sticker & Maker ───────────────────────────────────────────────────────

	case "tenor":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTenor(ctx, evt, args) })

	case "brat":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBrat(ctx, evt, args) })

	case "bratvid":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleBratVid(ctx, evt, args) })

	case "smeme":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleSmeme(ctx, evt, args) })

	case "iqc", "fakeiphonechat":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleIQC(ctx, evt, args) })

	case "qc", "fc", "fakechat", "quotecard":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleQuoteCard(ctx, evt, args) })

	case "tohitam", "toputih", "tozombie", "toroblox", "tomirror",
		"tochibi", "toghibli", "tojapanese", "tojepang", "tolego",
		"toreal", "totua", "tomoai", "tomonyet", "topacar", "toroh",
		"totato", "toviking", "tobotak", "tofunk", "tofigura", "tohijab",
		"tokacamata", "tokamboja", "toliquor", "tomaid", "topeci",
		"topiramida", "tounderground":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleFAAEffect(ctx, evt, cmd) })

	// ── Fitur Batch 1 ───────────────────────────────────────────────────────

	case "gempa", "bmkg", "infogempa", "earthquake":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleGempa(ctx, evt) })

	case "ipinfo", "ip", "iplookup", "whois":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleIPInfo(ctx, evt, args) })

	case "rate", "nilai", "rating":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleRate(ctx, evt, args) })

	case "cekkhodam", "khodam", "cekhodam":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekKhodam(ctx, evt, args) })

	case "quran", "surah", "alquran", "bacaquran":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleQuran(ctx, evt, args) })

	case "githubstalk", "ghstalk", "stalkgh":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleGitHubStalk(ctx, evt, args) })

	case "scanrepo", "scan":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleScanRepo(ctx, evt, args) })

	// ── Fitur Batch 2 ───────────────────────────────────────────────────────

	case "jadwalsholat", "sholat", "prayertime", "jadwalsalat":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleJadwalSholat(ctx, evt, args) })

	case "news", "beritanews":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleNewsRSS(ctx, evt, args) })

	case "npmstalk", "stalknpm":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleNPMStalk(ctx, evt, args) })

	case "ceknik", "nik", "nikparser":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekNIK(ctx, evt, args) })

	case "emojitoimage", "emojipng", "emojimg":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleEmojiToImage(ctx, evt, args) })

	case "tiktokstalk", "stalktiktok", "ttstalk":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTikTokStalk(ctx, evt, args) })

	case "howgay", "gaycheck":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleHowGay(ctx, evt, args) })

	case "mediafire", "mfdl", "mf":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleMediaFire(ctx, evt, args) })

	case "getpaste", "pastebin", "getpb":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleGetPaste(ctx, evt, args) })

	case "cekganteng", "ganteng", "handsome":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekGanteng(ctx, evt, args) })

	case "cekbucin", "bucin":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleCekBucin(ctx, evt, args) })

	case "robloxstalk", "rblxstalk", "rbxstalk", "stalkroblox":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleRobloxStalk(ctx, evt, args) })

	case "jodoh", "match", "ship":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleJodoh(ctx, evt, args) })

	// ── Fitur Batch 4 ───────────────────────────────────────────────────────

	case "murrotal", "murottal", "audioquran", "quraudio":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleMurrotal(ctx, evt, args) })

	case "meme", "randommeme":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleMeme(ctx, evt, args) })

	case "animeinfo", "animesearch", "anime":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleAnimeInfo(ctx, evt, args) })

	// ── Fitur Batch 5 ───────────────────────────────────────────────────────

	case "jadwalbola", "bola", "football", "jadwalsepakbola":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleJadwalBola(ctx, evt, args) })

	case "truth", "truthq":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTruth(ctx, evt, args) })

	case "dare":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleDare(ctx, evt, args) })

	case "dns":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleDNS(ctx, evt, args) })

	// ── Fitur Batch 6 ───────────────────────────────────────────────────────

	case "wastalk":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleWAStalk(ctx, evt, args) })

	case "lookup":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleLookup(ctx, evt, args) })

	case "dafont", "font", "daffont", "carifont":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleDaFont(ctx, evt, args) })

	case "editimg", "imgedit", "nanoedit":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleEditImg(ctx, evt, args) })

	case "ai":
		if !requireOwner(ctx, evt, ownerSender) {
			return
		}
		safeGo(func() { handleAiCmd(ctx, evt, args) })

	case "poll", "polling", "jajakpendapat":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handlePoll(ctx, evt, args) })

	case "kontak", "contact", "vcard":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleContact(ctx, evt, args) })

	case "lokasi", "location", "maps":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleLocation(ctx, evt, args) })

	case "vo", "viewonce":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleViewOnce(ctx, evt, args) })

	case "ptv":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handlePTV(ctx, evt, args) })

	case "edit":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleEdit(ctx, evt, args) })

	case "livlok", "livlocation", "sharelok":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleLiveLocation(ctx, evt, args) })

	case "template", "tbutton", "buttons":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTemplateMsg(ctx, evt, args) })

	case "tabel", "table", "sendtable":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleTable(ctx, evt, args) })

	case "doc", "document":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handleDoc(ctx, evt, args) })

	case "semat", "pinmsg", "pinchat":
		if !requirePublicOrOwner(ownerSender) {
			return
		}
		safeGo(func() { handlePin(ctx, evt, args) })
	}
}

func requirePublicOrOwner(owner bool) bool {
	return owner || getMode() == "public"
}

func requireOwner(ctx context.Context, evt *events.Message, owner bool) bool {
	if owner {
		return true
	}
	if getMode() == "public" {
		sendText(ctx, evt.Info.Chat, "🔒 Fitur ini cuma bisa dipakai *owner* bot.")
	}
	return false
}

// requireOwnerOrPremium — owner ATAU premium (database/premium.json) boleh
// pakai. Dipakai untuk !playcall dkk. Fitur admin (addsender dll) tetap
// requireOwner murni.
func requireOwnerOrPremium(ctx context.Context, evt *events.Message, ownerSender bool, number string) bool {
	if ownerSender || isPremiumDB(number) {
		return true
	}
	if getMode() == "public" {
		sendText(ctx, evt.Info.Chat, "🔒 Fitur ini cuma bisa dipakai *owner* atau *premium* bot.")
	}
	return false
}

// ─── Menu & Ping ─────────────────────────────────────────────────────────────

// menuText — tampilan ringkas & profesional untuk !menu.
// Format box-drawing ala gaya Airich (╭┈┈⫹⫺ box ┤ + │ ◈) — rapi, minim emoji.
// Di-generate dari menu_registry.go — tambah command baru = cukup daftarkan di registry.
// Gaya box konsisten untuk semua pesan menu — header & footer tertutup
// simetris (sebelumnya footer cuma "╰┈┈┈┈┈┈┈┈" yang tidak sejajar header).
const (
	boxTop = "╭┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈╮"
	boxBot = "╰┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈╯"
)

func menuText(ownerSender bool, ownerLevel bool, premiumSender bool) string {
	senders := pool.list()
	online := 0
	for _, s := range senders {
		if s.connected() {
			online++
		}
	}
	modeStr := "Public"
	if getMode() == "self" {
		modeStr = "Self"
	}
	p := Prefix

	var b strings.Builder

	// ── Box info bot — header/footer tertutup simetris ──────────────────────
	fmt.Fprintf(&b, "%s\n", boxTop)
	fmt.Fprintf(&b, "│  ⫹⫺ *%s* ⫹⫺\n", strings.ToUpper(BotName))
	fmt.Fprintf(&b, "│  ◈ Developer : %s\n", BotDeveloper)
	fmt.Fprintf(&b, "│  ◈ GitHub    : github.com/Riflxz\n")
	fmt.Fprintf(&b, "│  ◈ Mode      : %s\n", modeStr)
	fmt.Fprintf(&b, "│  ◈ Sender    : %d/%d online\n", online, len(senders))
	fmt.Fprintf(&b, "│  ◈ Uptime    : %s\n", formatDuration(time.Since(startTime)))
	fmt.Fprintf(&b, "%s\n\n", boxBot)

	// ── Daftar kategori + jumlah command — otomatis dari registry ───────────
	fmt.Fprintf(&b, "╭─☰ *KATEGORI* ☰─╮\n")
	for _, cat := range visibleCats(ownerSender, ownerLevel, premiumSender) {
		fmt.Fprintf(&b, "│  ◈ *%s* — %d command\n",
			strings.ToUpper(cat), catCount(cat, ownerSender, ownerLevel, premiumSender))
	}
	fmt.Fprintf(&b, "╰────────────────╯\n")

	// ── Box donasi — !donasi / !qris wajib terlihat di semua versi menu ─────
	fmt.Fprintf(&b, "\n%s\n", boxTop)
	fmt.Fprintf(&b, "│  ⫹⫺ *DUKUNG KAMI* ⫹⫺\n")
	fmt.Fprintf(&b, "│  ◈ Donasi   : `%sdonasi` / `%sqris`\n", p, p)
	fmt.Fprintf(&b, "%s\n\n", boxBot)

	fmt.Fprintf(&b, "_Ketik_ `%smenucat <kategori>` _untuk detail per kategori_\n", p)
	fmt.Fprintf(&b, "_Atau_ `%sallmenu` _untuk panduan lengkap + deskripsi_", p)
	return b.String()
}

// contributorText — daftar kontributor bot. Ditampilkan via !contributor
// (bisa diklik dari tombol/row menu), tidak lagi di-print inline di !menu.
func contributorText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", boxTop)
	fmt.Fprintf(&b, "│  ⫹⫺ *CONTRIBUTOR* ⫹⫺\n")
	fmt.Fprintf(&b, "│  ◈ kyu ganteng imut\n")
	fmt.Fprintf(&b, "│     ↳ t.me/kyugaperawan\n")
	fmt.Fprintf(&b, "│     ↳ t.me/kyunotdev\n")
	fmt.Fprintf(&b, "│     ↳ t.me/raramasihkyu\n")
	fmt.Fprintf(&b, "│  ◈ Rijalganzz — github.com/RIJALGANZZZ\n")
	fmt.Fprintf(&b, "│  ◈ Yamzzdep — github.com/Yamzzdev\n")
	fmt.Fprintf(&b, "│  ◈ Ryuhan — github.com/ryuhandev\n")
	fmt.Fprintf(&b, "│  ◈ Developer — github.com/Riflxz\n")
	fmt.Fprintf(&b, "%s", boxBot)
	return b.String()
}

// menuCatListText — versi RINGKAS menuText untuk pesan interaktif (V3/V4):
// daftar kategori + donasi + tips, TANPA box info bot (header pesan sudah
// memuat nama bot & developer — ngeprint ulang cuma bikin panjang).
func menuCatListText(ownerSender, ownerLevel, premiumSender bool) string {
	p := Prefix
	var b strings.Builder

	fmt.Fprintf(&b, "╭─☰ *KATEGORI* ─╮\n")
	for _, cat := range visibleCats(ownerSender, ownerLevel, premiumSender) {
		fmt.Fprintf(&b, "│ ◈ *%s* — %d command\n",
			strings.ToUpper(cat), catCount(cat, ownerSender, ownerLevel, premiumSender))
	}
	fmt.Fprintf(&b, "╰─⬣\n")

	fmt.Fprintf(&b, "\n╭┈┈⫹⫺ *DUKUNG KAMI* ⫹⫺┈┈╮\n")
	fmt.Fprintf(&b, "│ ◈ Donasi : `%sdonasi` / `%sqris`\n", p, p)
	fmt.Fprintf(&b, "╰┈┈┈┈┈┈┈┈\n\n")

	fmt.Fprintf(&b, "_Pilih kategori di tombol di bawah, atau_ `%smenucat <kategori>`", p)
	return b.String()
}

// allMenuText — daftar lengkap semua command dengan penjelasan untuk !allmenu
// Dapat diakses semua user — ini hanya referensi, bukan eksekusi.
// Di-generate dari menu_registry.go.
func allMenuText(ownerSender bool, ownerLevel bool, premiumSender bool) string {
	p := Prefix
	var b strings.Builder

	fmt.Fprintf(&b, "╭┈┈⫹⫺ *%s — PANDUAN LENGKAP* ⫹⫺┈┈╮\n", strings.ToUpper(BotName))
	fmt.Fprintf(&b, "│ ◈ Prefix    : `%s`\n", p)
	fmt.Fprintf(&b, "│ ◈ Developer : %s\n", BotDeveloper)
	fmt.Fprintf(&b, "╰┈┈┈┈┈┈┈┈\n\n")

	for _, cat := range visibleCats(ownerSender, ownerLevel, premiumSender) {
		fmt.Fprintf(&b, "╭─☰ *%s*\n", strings.ToUpper(cat))
		for _, c := range cmdsByCat(ownerSender, ownerLevel, premiumSender)[cat] {
			fmt.Fprintf(&b, "> `%s%s` — %s\n", p, c.Name, c.Desc)
		}
		fmt.Fprintf(&b, "╰─⬣\n\n")
	}

	return b.String()
}

// menuCatText — isi satu kategori untuk !menucat <kategori>.
func menuCatText(cat string, ownerSender bool, ownerLevel bool, premiumSender bool) string {
	p := Prefix
	var b strings.Builder

	fmt.Fprintf(&b, "╭─☰ *%s*\n", strings.ToUpper(cat))
	for _, c := range cmdsByCat(ownerSender, ownerLevel, premiumSender)[cat] {
		fmt.Fprintf(&b, "> `%s%s` — %s\n", p, c.Name, c.Desc)
	}
	fmt.Fprintf(&b, "╰─⬣\n")
	return b.String()
}

// handleMenuCat — !menucat <kategori> → tampilkan command dalam kategori itu.
func handleMenuCat(ctx context.Context, evt *events.Message, args string, ownerSender bool, ownerLevel bool, premiumSender bool) {
	key := strings.TrimSpace(args)
	if key == "" {
		// Tanpa argumen → daftar kategori yang tersedia.
		var b strings.Builder
		fmt.Fprintf(&b, "╭─☰ *KATEGORI* ─╮\n")
		for _, cat := range visibleCats(ownerSender, ownerLevel, premiumSender) {
			fmt.Fprintf(&b, "│ ◈ *%s* — %d command\n",
				strings.ToUpper(cat), catCount(cat, ownerSender, ownerLevel, premiumSender))
		}
		fmt.Fprintf(&b, "╰─⬣\n")
		fmt.Fprintf(&b, "\n_Ketik_ `%smenucat <kategori>` _untuk lihat isinya_", Prefix)
		sendText(ctx, evt.Info.Chat, b.String())
		return
	}

	cat := catByKey(key)
	if cat == "" {
		var valid []string
		for _, c := range visibleCats(ownerSender, ownerLevel, premiumSender) {
			valid = append(valid, c)
		}
		sendText(ctx, evt.Info.Chat, fmt.Sprintf(
			"❌ Kategori *%s* tidak ditemukan.\nKategori yang tersedia: `%s`",
			key, strings.Join(valid, "`, `")))
		return
	}

	sendText(ctx, evt.Info.Chat, menuCatText(cat, ownerSender, ownerLevel, premiumSender))
}

func handlePing(ctx context.Context, evt *events.Message) {
	delay := time.Since(evt.Info.Timestamp)
	if delay < 0 {
		delay = 0
	}
	senders := pool.list()
	online := 0
	for _, s := range senders {
		if s.connected() {
			online++
		}
	}

	// ── Statistik sistem (stdlib, tanpa dep eksternal) ─────────────────────
	hostname, _ := os.Hostname()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	totalRAM, freeRAM := readMemInfo()
	usedRAM := totalRAM - freeRAM

	diskTotal, diskUsed := readDiskUsage(".")

	botUp := formatDuration(time.Since(startTime))
	serverUp := formatDuration(time.Duration(readUptimeSeconds()) * time.Second)

	cpuLoad := readLoadAvg()
	cpuPct := "N/A"
	if c := runtime.NumCPU(); c > 0 {
		if l, err := strconv.ParseFloat(cpuLoad, 64); err == nil {
			cpuPct = fmt.Sprintf("%.1f%%", l/float64(c)*100)
		}
	}

	// DB stats: owner & premium dari file JSON yang ada.
	ownerCount := len(readNumbersDB(OwnerDBPath))
	premiumCount := len(readNumbersDB(PremiumDBPath))

	// Network iface aktif (skip loopback).
	iface := "N/A"
	if addrs, err := net.Interfaces(); err == nil {
		for _, a := range addrs {
			if a.Flags&net.FlagLoopback != 0 {
				continue
			}
			if a.Flags&net.FlagUp != 0 {
				iface = a.Name
				break
			}
		}
	}

	rows := [][2]string{
		{"WA Roundtrip", fmt.Sprintf("%d ms", delay.Milliseconds())},
		{"Kecepatan Respon", fmt.Sprintf("%d ms", delay.Milliseconds())},
		{"Status", "Online"},
		{"Hostname", hostname},
		{"Platform", runtime.GOOS + "/" + runtime.GOARCH},
		{"Go", runtime.Version()},
		{"CPU", readCPUModel()},
		{"Cores", strconv.Itoa(runtime.NumCPU())},
		{"CPU Load", cpuPct},
		{"RAM", formatBytes(usedRAM) + " / " + formatBytes(totalRAM)},
		{"Heap", formatBytes(mem.HeapInuse) + " / " + formatBytes(mem.HeapSys)},
		{"Disk", formatBytes(diskUsed) + " / " + formatBytes(diskTotal)},
		{"Network", iface},
		{"Owner", strconv.Itoa(ownerCount)},
		{"Premium", strconv.Itoa(premiumCount)},
		{"Groups", "N/A"},
		{"Uptime Bot", botUp},
		{"Uptime Server", serverUp},
	}

	table := make([][]string, 0, len(rows)+1)
	table = append(table, []string{"Metric", "Value"})
	for _, r := range rows {
		table = append(table, []string{r[0], r[1]})
	}

	if _, err := NewAIRich().
		SetTitle("⚡️ "+BotName+" — System Performance").
		AddTable(table).
		SetFooter("Performa Realtime "+BotName).
		Send(ctx, evt.Info.Chat); err != nil {
		sendText(ctx, evt.Info.Chat, "❌ Gagal kirim statistik: "+err.Error())
	}
}

// readUptimeSeconds — uptime server (detik) dari /proc/uptime.
func readUptimeSeconds() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	var up float64
	fmt.Sscanf(fields[0], "%f", &up)
	return up
}

// readCPUModel — nama CPU dari /proc/cpuinfo.
func readCPUModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "N/A"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "N/A"
}

// readMemInfo — baca total & free RAM dari /proc/meminfo (Linux).
func readMemInfo() (total, free uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var memTotal, memFree, buffers, cached uint64
	for _, line := range strings.Split(string(data), "\n") {
		var key string
		var val uint64
		if _, err := fmt.Sscanf(line, "%s %d", &key, &val); err != nil {
			continue
		}
		switch key {
		case "MemTotal:":
			memTotal = val * 1024
		case "MemFree:":
			memFree = val * 1024
		case "Buffers:":
			buffers = val * 1024
		case "Cached:":
			cached = val * 1024
		}
	}
	return memTotal, memFree + buffers + cached
}

// readDiskUsage — baca total & used disk via statfs.
func readDiskUsage(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	used = total - free
	return total, used
}

// readLoadAvg — baca load average 1 menit dari /proc/loadavg.
func readLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "N/A"
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "N/A"
	}
	return fields[0]
}

// formatBytes — format ukuran byte ke satuan manusiawi.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ─── Queue commands ──────────────────────────────────────────────────────────

func handleQueue(ctx context.Context, evt *events.Message, args string, ownerSender bool) {
	sess, msg := pool.sessionFor(strings.TrimSpace(args))
	if sess == nil {
		sendText(ctx, evt.Info.Chat, msg)
		return
	}
	// Fix DL-01: kumpulkan data di dalam lock, kirim di luar lock
	// agar sendText (blocking network) tidak menahan sess.mu.
	// Fix PRIVACY: nomor target cuma ditampilkan ke owner — di public mode
	// !queue bisa dipakai semua orang dan tidak boleh bocorkan nomor.
	sess.mu.Lock()
	var b strings.Builder
	if ownerSender {
		fmt.Fprintf(&b, "📋 *Antrian Lagu*\n📞 +%s | 🤖 %s | 🔖 `%s`\n\n", sess.target, sess.sender.name, sess.shortID)
	} else {
		fmt.Fprintf(&b, "📋 *Antrian Lagu*\n🤖 %s | 🔖 `%s`\n\n", sess.sender.name, sess.shortID)
	}
	if len(sess.queue) == 0 {
		fmt.Fprintf(&b, "_antrian kosong_")
	} else {
		for i, item := range sess.queue {
			fmt.Fprintf(&b, "%d. *%s* - %s (%s)\n", i+1, item.track.Title, item.track.Artist, item.track.Duration)
		}
	}
	text := b.String()
	sess.mu.Unlock()
	sendText(ctx, evt.Info.Chat, text)
}

// handleAntri tampilkan antrian yang belum diputar, plus lagu yang sedang
// diputar saat ini. Format lebih informatif dari m!antrian.
func handleAntri(ctx context.Context, evt *events.Message, args string) {
	sess, msg := pool.sessionFor(strings.TrimSpace(args))
	if sess == nil {
		sendText(ctx, evt.Info.Chat, msg)
		return
	}

	sess.mu.Lock()
	nowPlaying := sess.nowTitle
	target := sess.target
	senderName := sess.sender.name
	shortID := sess.shortID
	queue := append([]queueItem(nil), sess.queue...) // salin biar aman
	playerIdle := sess.player == nil || sess.player.State() == meowcaller.PlayerIdle
	sess.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "╔═ 『 📋 ANTRIAN LAGU 』\n")
	fmt.Fprintf(&b, "║ 📞 +%s  •  🤖 %s  •  🔖 `%s`\n", target, senderName, shortID)
	fmt.Fprintf(&b, "╠══════════════════════════\n")

	// Lagu yang sedang diputar
	if nowPlaying != "" && !playerIdle {
		fmt.Fprintf(&b, "║ ▶️ *Sedang diputar:*\n")
		fmt.Fprintf(&b, "║    %s\n", nowPlaying)
		fmt.Fprintf(&b, "╠══════════════════════════\n")
	} else if playerIdle {
		fmt.Fprintf(&b, "║ ⏸️ _Player sedang idle_\n")
		fmt.Fprintf(&b, "╠══════════════════════════\n")
	}

	// Antrian pending
	if len(queue) == 0 {
		fmt.Fprintf(&b, "║ _Antrian kosong_\n")
		fmt.Fprintf(&b, "║\n")
		fmt.Fprintf(&b, "║ Tambah lagu:\n")
		fmt.Fprintf(&b, "║ *%scontinue <judul>*\n", Prefix)
	} else {
		fmt.Fprintf(&b, "║ *Menunggu diputar (%d lagu):*\n", len(queue))
		for i, item := range queue {
			requester := senderUser(item.evt)
			fmt.Fprintf(&b, "║ %d. *%s*\n", i+1, item.track.Title)
			fmt.Fprintf(&b, "║    %s • %s • req: +%s\n", item.track.Artist, item.track.Duration, requester)
		}
		fmt.Fprintf(&b, "║\n")
		fmt.Fprintf(&b, "║ Hapus: *%shapus <judul>*\n", Prefix)
	}
	fmt.Fprintf(&b, "╚══════════════════════════")

	sendText(ctx, evt.Info.Chat, b.String())
}

// handleHapus hapus satu lagu dari antrian berdasarkan pencocokan judul
// (case-insensitive, substring match). Kalau ada beberapa yang cocok,
// hapus yang pertama dan kasih info sisanya.
func handleHapus(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat

	// Pisahkan judul dan optional sender (arg terakhir = "senderN")
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) == 0 {
		sendText(ctx, chat, fmt.Sprintf("❌ Format: *%shapus <judul>*", Prefix))
		return
	}
	senderArg := ""
	titleQuery := args
	if len(fields) > 1 {
		last := strings.ToLower(fields[len(fields)-1])
		if strings.HasPrefix(last, "sender") && pool.byName(last) != nil {
			senderArg = last
			titleQuery = strings.TrimSpace(strings.Join(fields[:len(fields)-1], " "))
		}
	}

	sess, msg := pool.sessionFor(senderArg)
	if sess == nil {
		sendText(ctx, chat, msg)
		return
	}

	query := strings.ToLower(strings.TrimSpace(titleQuery))

	// Fix DL-01: kumpulkan data di dalam lock, kirim pesan DI LUAR lock —
	// sendText (blocking network) tidak boleh menahan sess.mu.
	sess.mu.Lock()

	if len(sess.queue) == 0 {
		sess.mu.Unlock()
		sendText(ctx, chat, "📭 Antrian kosong, tidak ada yang bisa dihapus.")
		return
	}

	// Cari semua yang cocok (substring, case-insensitive)
	var matchIdx []int
	for i, item := range sess.queue {
		if strings.Contains(strings.ToLower(item.track.Title), query) {
			matchIdx = append(matchIdx, i)
		}
	}

	if len(matchIdx) == 0 {
		sess.mu.Unlock()
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Lagu *%s* tidak ditemukan di antrian.\nKetik *%santri* untuk lihat daftar.", titleQuery, Prefix))
		return
	}

	// Hapus match pertama
	idx := matchIdx[0]
	removed := sess.queue[idx]
	sess.queue = append(sess.queue[:idx], sess.queue[idx+1:]...)
	os.Remove(removed.file) // hapus file temp-nya sekalian

	var b strings.Builder
	fmt.Fprintf(&b, "🗑️ *Dihapus dari antrian:*\n")
	fmt.Fprintf(&b, "🎵 *%s* - %s (%s)\n", removed.track.Title, removed.track.Artist, removed.track.Duration)

	// Kalau ada lebih dari 1 yang cocok, kasih info sisanya masih ada
	if len(matchIdx) > 1 {
		fmt.Fprintf(&b, "\n_Ada %d lagu lain dengan nama mirip yang masih di antrian._", len(matchIdx)-1)
	}

	// Info sisa antrian
	if len(sess.queue) == 0 {
		fmt.Fprintf(&b, "\n📭 Antrian sekarang kosong.")
	} else {
		fmt.Fprintf(&b, "\n📋 Sisa antrian: *%d lagu*", len(sess.queue))
	}
	sess.mu.Unlock()

	sendText(ctx, chat, b.String())
	reactMsg(ctx, evt, "🗑️")
}

func handleSkip(ctx context.Context, evt *events.Message, args string) {
	sess, msg := pool.sessionFor(strings.TrimSpace(args))
	if sess == nil {
		sendText(ctx, evt.Info.Chat, msg)
		return
	}
	sess.mu.Lock()
	if sess.player == nil {
		sess.mu.Unlock()
		sendText(ctx, evt.Info.Chat, "📭 Belum ada yang diputar.")
		return
	}
	p := sess.player
	fn := sess.playNext
	hasQueue := len(sess.queue) > 0
	// Fix PRANK-SKIP: p.Stop() tidak memicu OnFinish (meowcaller/player.go),
	// jadi tanpa reset manual pranking tetap true — OnFinish lagu berikutnya
	// bakal salah mengira prank baru selesai, me-replay curFile (lagu yang
	// barusan di-skip) dan menghapus prankFile.
	if sess.pranking {
		sess.pranking = false
		if pf := sess.prankFile; pf != "" {
			sess.prankFile = ""
			os.Remove(pf)
		}
	}
	sess.mu.Unlock()

	// Fix LB-03: skip tetap bisa dipakai meski antrian kosong —
	// menghentikan lagu saat ini, call tetap aktif, player jadi idle.
	reactMsg(ctx, evt, "⏭️")
	p.Stop()
	if hasQueue && fn != nil {
		safeGo(fn)
	} else {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf(
			"⏭️ Lagu dilewati.\n📭 Antrian kosong — tambah: *%scontinue <judul>*", Prefix))
	}
}

func handleStop(ctx context.Context, evt *events.Message, args string) {
	sess, msg := pool.sessionFor(strings.TrimSpace(args))
	if sess == nil {
		sendText(ctx, evt.Info.Chat, msg)
		return
	}
	// Fix: JANGAN reset manual di sini — reset() dulu dipakai langsung, tapi itu
	// membuat stopHB/done tidak pernah di-close (goroutine handlePlaycall & heartbeat
	// bocor) dan snd.sess tidak dilepas. Pakai doCleanup yang idempotent
	// (cleanupOnce): hapus file, reset, snd.sess=nil, stopAll sekaligus.
	sess.mu.Lock()
	call := sess.call
	name := sess.sender.name
	sess.mu.Unlock()

	reactMsg(ctx, evt, "🛑")
	sendText(ctx, evt.Info.Chat, "🛑 Call *"+name+"* dihangup & antrian dibersihkan.")
	if call != nil {
		_ = call.Hangup() // → OnEnd → doCleanup (double-call aman karena cleanupOnce)
	}
	sess.doCleanup(sess.sender)
}

// ─── Playcall ────────────────────────────────────────────────────────────────

// normalizeJIDString parse sebuah JID string dan strip bagian device/AD-nya
// (misal "166692462301357:74@lid" -> "166692462301357@lid"). Ini PENTING:
// pesan yang di-reply di grup kadang ngasih Participant JID yang masih nempel
// device-suffix (AD) dari device tertentu si pengirim. Kalau suffix itu kebawa
// ke target call, WhatsApp kadang nyasar nyari device yang udah gak aktif --
// makanya call "kadang bisa kadang ga" tergantung device mana yang lagi
// online. Stripping ke NonAD (bare LID/JID) bikin resolusi konsisten.
func normalizeJIDString(raw string) string {
	if raw == "" {
		return raw
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return raw
	}
	return jid.ToNonAD().String()
}

// resolveLIDToPhone: kalau JID-nya format @lid (WhatsApp LID/privacy ID, BUKAN nomor
// telepon), coba resolve ke nomor asli pakai LID store bawaan whatsmeow. Ini WAJIB
// buat .playcall/.playvideo — CallVideo/Call butuh NOMOR TELEPON asli, bukan LID,
// buat bisa benar-benar nelpon. Reply chat di grup SERING ngasih JID format @lid
// (fitur privasi WA yang nyembunyiin nomor asli di daftar partisipan grup), jadi ini
// resolusi WAJIB, bukan optional. Kalau gagal resolve (belum ada di cache whatsmeow),
// balikin JID aslinya + error biar caller bisa kasih pesan yang jelas ke user.
func resolveLIDToPhone(ctx context.Context, jid types.JID) (types.JID, error) {
	if jid.Server != types.HiddenUserServer { // bukan @lid, langsung balikin apa adanya
		return jid, nil
	}
	pn, err := waClient.Store.LIDs.GetPNForLID(ctx, jid)
	if err != nil {
		return jid, fmt.Errorf("gagal resolve LID ke nomor: %w", err)
	}
	if pn.IsEmpty() || pn.User == "" {
		return jid, fmt.Errorf("nomor asli kontak belum tersedia (LID: %s) — minta kontak chat bot sekali, atau reply pesan lain dari orang yang sama, lalu coba lagi", jid.User)
	}
	return pn, nil
}

// displayUser tampilkan identitas user di pesan bot secara rapi & profesional:
// LID di-resolve ke nomor asli kalau tersedia (fallback: JID bare), format
// internasional "+62xxx" tanpa suffix server.
func displayUser(ctx context.Context, jid types.JID) string {
	if resolved, err := resolveLIDToPhone(ctx, jid); err == nil && !resolved.IsEmpty() && resolved.User != "" {
		jid = resolved
	}
	return "+" + jid.ToNonAD().User
}

func resolveTarget(ctx context.Context, evt *events.Message, args string) (target, title string, err error) {
	ctxInfo := msgContextInfo(evt)
	quoted := ctxInfo.GetParticipant()

	if quoted == "" && ctxInfo.GetStanzaID() != "" && !evt.Info.IsGroup {
		quoted = evt.Info.Chat.String()
	}

	if quoted != "" {
		quoted = normalizeJIDString(quoted)
		jid, perr := types.ParseJID(quoted)
		if perr == nil {
			resolved, lerr := resolveLIDToPhone(ctx, jid)
			if lerr != nil {
				return "", "", lerr
			}
			quoted = resolved.ToNonAD().String()
		}
		title = strings.TrimSpace(args)
		if title == "" {
			return "", "", fmt.Errorf("judul kosong. contoh: reply chat orangnya + \"%splaycall Bertaut\"", Prefix)
		}
		return quoted, title, nil
	}
	parts := strings.SplitN(args, ",", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("format salah.\n• reply + \"%splaycall <judul>\"\n• atau \"%splaycall 628xxxx, <judul>\"", Prefix, Prefix)
	}
	number := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parts[0]), "+"))
	number = strings.NewReplacer(" ", "", "-", "").Replace(number)
	title = strings.TrimSpace(parts[1])
	if number == "" || title == "" {
		return "", "", fmt.Errorf("nomor atau judul kosong")
	}
	return number, title, nil
}

// validateMP3 sekarang juga terima WAV (output processAudioForCall)
func validateMP3(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 4096 {
		return fmt.Errorf("file terlalu kecil (%d bytes), kemungkinan corrupt", info.Size())
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(f, header); err != nil {
		return err
	}
	// MP3 : ID3 (0x49) atau frame sync (0xFF)
	// WAV : RIFF (52 49 46 46)
	// RAW : s16le PCM — file size minimal = audio ada
	// Tidak ada magic bytes untuk raw PCM, jadi skip header check
	if strings.HasSuffix(path, ".raw") {
		return nil // raw PCM tidak punya magic bytes, size check sudah cukup
	}
	isMP3 := header[0] == 0x49 || header[0] == 0xFF
	isWAV := header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46
	if !isMP3 && !isWAV {
		return fmt.Errorf("format audio tidak dikenal (header: %x)", header[:4])
	}
	return nil
}

func handlePlaycall(ctx context.Context, evt *events.Message, args string) {
	reactMsg(ctx, evt, "⏳")
	chat := evt.Info.Chat

	if ok, wait := checkCooldown(senderUser(evt)); !ok {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("⏳ Cooldown, tunggu %ds lagi.", int(wait.Seconds())+1))
		return
	}

	// ── Mode title-only: tidak ada reply & tidak ada koma ─────────────────────
	// Kalau user cuma ketik "m!playcall <judul>" tanpa reply/nomor, cek apakah
	// ada call aktif. Kalau ada satu → langsung tambah ke antrian call itu.
	// Ini fix utama: user tidak perlu reply chat lagi kalau sudah dalam call.
	ctxInfoCheck := msgContextInfo(evt)
	hasReply := ctxInfoCheck.GetParticipant() != "" || ctxInfoCheck.GetStanzaID() != ""
	hasComma := strings.Contains(args, ",")
	trimmedArgs := strings.TrimSpace(args)

	if !hasReply && !hasComma && trimmedArgs != "" {
		active := pool.activeSessions()
		if len(active) == 1 {
			safeGo(func() { addSongToSession(ctx, evt, active[0], trimmedArgs) })
			return
		} else if len(active) > 1 {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, fmt.Sprintf(
				"❓ Ada %d call aktif. Pakai reply atau format:\n*%splaycall 628xxx, <judul>*", len(active), Prefix))
			return
		}
		// len == 0: tidak ada call aktif, lanjut ke resolveTarget biasa
		// (akan error "format salah" yang menjelaskan cara pakainya)
	}

	target, title, err := resolveTarget(ctx, evt, args)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	cleanTarget := cleanJIDNumber(target)

	// Kalau target ini udah lagi ditelpon salah satu sender → lagunya masuk antrian
	// call itu (biar gak dobel-telpon orang yang sama).
	if existing := findSessionByTarget(cleanTarget); existing != nil {
		safeGo(func() { addSongToSession(ctx, evt, existing, title) })
		return
	}

	// Ambil sender yang nganggur + reserve ATOMIK dalam satu lock.
	// Fix TOCTOU: pola lama (acquireFree lalu set snd.sess manual) punya celah —
	// dua .playcall konkuren bisa dapat sender yang SAMA dan saling menimpa.
	// acquireReserveAudio cek & attach sekaligus, jadi sender yang sudah
	// di-reserve langsung dianggap sibuk oleh request berikutnya.
	sess := &callSession{
		active:    true,
		target:    cleanTarget,
		requester: senderUser(evt),
		stopHB:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	snd := pool.acquireReserveAudio(sess)
	if snd == nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Semua sender lagi sibuk nelpon / gak ada yang online.\nTambah sender: *%saddsender 628xxxx*", Prefix))
		return
	}

	track, err := fetchPlayTrack(title)
	if err != nil {
		releaseSession(snd, sess)
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ lagu gak ketemu: "+err.Error())
		return
	}

	file, err := downloadAudio(track.DownloadURL)
	if err != nil {
		releaseSession(snd, sess)
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ gagal download: "+err.Error())
		return
	}
	if err := validateMP3(file); err != nil {
		os.Remove(file)
		releaseSession(snd, sess)
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ audio corrupt, coba judul lain: "+err.Error())
		return
	}

	item := queueItem{track: track, file: file, evt: evt}
	sess.mu.Lock()
	sess.queue = []queueItem{item}
	sess.mu.Unlock()

	// Info play + tombol kontrol (quick_reply: skip/stop) — fallback teks
	// biasa kalau interactive gagal. "lanjut" tidak perlu tombol: tombol
	// skip/stop bisa dipakai, dan antrian jalan otomatis.
	ctrl := NewMsgBuilder().
		SetHeader("🎵 PLAY CALL", snd.name+" → "+cleanTarget).
		SetBody(fmt.Sprintf("*%s* - %s\n📞 %s nelpon *+%s*...", track.Title, track.Artist, snd.name, cleanTarget)).
		SetFooter("Owner only").
		AddQRButton("⏭️ Skip", Prefix+"skip "+snd.name).
		AddQRButton("🛑 Stop", Prefix+"stopcall "+snd.name)
	if sendErr := ctrl.Send(ctx, chat); sendErr != nil {
		pool.logger.Warn().Err(sendErr).Msg("playcall: kontrol interactive gagal, fallback teks")
		sendText(ctx, chat, fmt.Sprintf("🎵 *%s* - %s\n📞 %s nelpon *+%s*...\n\n⏭️ Skip: *%sskip %s* | 🛑 Stop: *%sstopcall %s*",
			track.Title, track.Artist, snd.name, cleanTarget, Prefix, snd.name, Prefix, snd.name))
	}
	pool.logger.Debug().Str("sender", snd.name).Str("target", cleanTarget).Msg("playcall resolved target")

	// Fix PHANTOM-CALL: kalau session di-cleanup saat proses download (mis.
	// !stopcall datang ketika lagu masih 30-90 detik di-download), doCleanup
	// sudah menutup sess.done & me-reset session. Kalau kita tetap lanjut dial,
	// call keluar TANPA ada yang bisa menghentikannya: dialTimer keluar via
	// <-sess.done tanpa Hangup, dan goroutine utama langsung return via
	// <-sess.done — call ring selamanya dan file antrian bocor.
	sess.mu.Lock()
	stillActive := sess.active
	sess.mu.Unlock()
	if !stillActive {
		os.Remove(file)
		sendText(ctx, chat, "🛑 Call dibatalkan sebelum tersambung.")
		return
	}

	var call *meowcaller.Call
	for attempt := 1; attempt <= 2; attempt++ {
		callCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		call, err = snd.call.Call(callCtx, target)
		cancel()
		if err == nil {
			break
		}
		pool.logger.Warn().Str("sender", snd.name).Int("attempt", attempt).Err(err).Msg("playcall attempt gagal")
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ gagal nelpon +"+cleanTarget+": "+err.Error())
		os.Remove(file)
		releaseSession(snd, sess)
		return
	}

	// Set sess.call SEGERA setelah Call sukses — kalau !stopcall datang di
	// celah ini, handleStop menemukan call yang bisa dihangup. Setelah itu baru
	// cek active: doCleanup yang sempat jalan sebelumnya membuat kita batalkan
	// dial ini (kalau tidak, call ring tanpa kontrol — phantom call).
	shortID := call.ID()
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	sess.mu.Lock()
	sess.call = call
	sess.shortID = shortID
	stillActive = sess.active
	sess.mu.Unlock()

	if !stillActive {
		_ = call.Hangup()
		os.Remove(file)
		sendText(ctx, chat, "🛑 Call dibatalkan sebelum tersambung.")
		return
	}

	go keepCallAlive(snd.wa, call.ID(), sess.stopHB)

	// ── playNext closure ──────────────────────────────────────────────────────
	var playNextFn func()
	playNextFn = func() {
		// Fix: satu playNextFn yang berjalan pada satu waktu — cegah double-pop
		// & double-Play (lihat komentar playMu di struct callSession).
		sess.playMu.Lock()
		defer sess.playMu.Unlock()
		sess.mu.Lock()
		if !sess.active || len(sess.queue) == 0 {
			sess.mu.Unlock()
			sendText(ctx, chat, fmt.Sprintf(
				"🎵 Semua lagu habis.\n📞 Call %s *+%s* masih aktif.\n➕ Tambah: *%splaycall <judul>*",
				snd.name, cleanTarget, Prefix))
			return
		}
		next := sess.queue[0]
		sess.queue = sess.queue[1:]
		p := sess.player
		prevFile := sess.curFile
		sess.mu.Unlock()

		if p == nil {
			os.Remove(next.file)
			return
		}

		// Pilih decoder sesuai ekstensi file
		var src meowcaller.AudioSource
		switch {
		case strings.HasSuffix(next.file, ".raw"):
			// Pre-load seluruh file ke RAM sebelum play.
			// Fix choppy: eliminasi disk I/O latency saat real-time call.
			var data []byte
			data, err = os.ReadFile(next.file)
			if err == nil {
				src = meowcaller.PCMStream(io.NopCloser(bytes.NewReader(data)))
			}
		case strings.HasSuffix(next.file, ".wav"):
			src, err = meowcaller.WAVFile(next.file)
		default:
			src, err = meowcaller.MP3File(next.file)
		}
		if err != nil {
			sendText(ctx, chat, fmt.Sprintf("❌ gagal load *%s*: %v", next.track.Title, err))
			os.Remove(next.file)
			safeGo(playNextFn)
			return
		}

		// Simpen judul + file yang lagi diputar (buat prank resume). File lagu
		// SEBELUMnya baru dihapus sekarang (biar curFile masih valid selama diputer).
		sess.mu.Lock()
		sess.nowTitle = next.track.Title
		sess.curFile = next.file
		sess.mu.Unlock()
		if prevFile != "" && prevFile != next.file {
			os.Remove(prevFile)
		}

		sendText(ctx, chat, fmt.Sprintf(
			"▶️ *Now Playing*\n🎵 *%s* - %s (%s)\n🤖 %s | 🔖 `%s`",
			next.track.Title, next.track.Artist, next.track.Duration, snd.name, shortID))

		p.Play(src)
	}

	connected := make(chan struct{}, 1)

	call.OnReady(func() {
		p := meowcaller.NewPlayer()
		call.Subscribe(p)

		p.OnFinish(func() {
			// Kalau lagi prank, OnFinish-nya audio prank → balikin lagu asli.
			// Fix RC-FILE-03: hapus prankFile setelah prank selesai.
			sess.mu.Lock()
			if sess.pranking {
				sess.pranking = false
				cf := sess.curFile
				pl := sess.player
				pf := sess.prankFile
				sess.prankFile = ""
				sess.mu.Unlock()
				if pf != "" {
					os.Remove(pf) // cleanup file temp prank
				}
				if cf != "" && pl != nil {
					loadAudio := func(path string) (meowcaller.AudioSource, error) {
						switch {
						case strings.HasSuffix(path, ".raw"):
							data, ferr := os.ReadFile(path)
							if ferr != nil {
								return nil, ferr
							}
							return meowcaller.PCMStream(io.NopCloser(bytes.NewReader(data))), nil
						case strings.HasSuffix(path, ".wav"):
							return meowcaller.WAVFile(path)
						default:
							return meowcaller.MP3File(path)
						}
					}
					if src, e := loadAudio(cf); e == nil {
						sendText(ctx, chat, "😹 Prank selesai, lagu dilanjutkan.")
						pl.Play(src)
						return
					}
				}
				playNextFn()
				return
			}
			sess.mu.Unlock()
			playNextFn()
		})

		sess.mu.Lock()
		sess.player = p
		sess.playNext = playNextFn
		sess.mu.Unlock()

		sendText(ctx, chat, fmt.Sprintf("✅ *%s tersambung ke +%s*\n🔖 `%s`", snd.name, cleanTarget, shortID))
		reactMsg(ctx, evt, "✅")

		select {
		case connected <- struct{}{}:
		default:
		}

		playNextFn()
	})

	call.OnEnd(func(reason string) {
		label := map[string]string{
			"timeout":      "timeout (gak diangkat)",
			"media_failed": "koneksi media gagal (relay gak nyambung)",
			"hangup":       "call ditutup",
			"":             "call ditutup",
		}[reason]
		if label == "" {
			label = reason
		}
		sendText(ctx, chat, fmt.Sprintf("📴 *Call %s Selesai*\n🔖 `%s` | %s", snd.name, shortID, label))
		reactMsg(ctx, evt, "📴")
		// Fix RC-02: cleanup via doCleanup, dijamin hanya jalan sekali
		// meski goroutine timeout juga memanggil doCleanup bersamaan.
		sess.doCleanup(snd)
	})

	// Fix stuck-session: call bisa sudah berakhir (di-reject / terminate) DI CELAH
	// antara Call() return dan OnEnd terdaftar di atas — meowcaller hanya memanggil
	// onEnd saat fase berubah, jadi callback yang terdaftar telat TIDAK AKAN pernah
	// fire. Akibatnya sess.done/stopHB tidak pernah di-close, sender dianggap sibuk
	// selamanya, dan goroutine bocor. Deteksi sekarang: kalau State() sudah Ended,
	// langsung jalankan jalur cleanup yang sama.
	if call.State() == meowcaller.CallPhaseEnded {
		pool.logger.Warn().Str("call_id", call.ID()).Msg("playcall: call sudah ended sebelum OnEnd terdaftar, cleanup manual")
		sendText(ctx, chat, fmt.Sprintf("📴 *Call %s ditutup*\n🔖 `%s`", snd.name, shortID))
		sess.doCleanup(snd)
	}

	// Fix GL-02: time.NewTimer agar timer bisa di-Stop (tidak leak).
	// Fix MISC-01: pakai ctx dari main supaya bisa di-cancel saat shutdown.
	// Fix: select juga pada sess.done supaya goroutine langsung keluar
	// saat cleanup terjadi (OnEnd / handleStop), tidak nunggu 45 detik.
	go func() {
		dialTimer := time.NewTimer(45 * time.Second)
		defer dialTimer.Stop()
		select {
		case <-connected:
		case <-sess.done:
		case <-ctx.Done():
			sess.doCleanup(snd)
			_ = call.Hangup()
		case <-dialTimer.C:
			// Cek done dulu (non-blocking) — kalau sudah di-cleanup OnEnd, skip.
			select {
			case <-sess.done:
			default:
				sendText(ctx, chat, fmt.Sprintf("📵 *+%s* gak ngangkat (45s)\n🤖 %s | 🔖 `%s`", cleanTarget, snd.name, shortID))
				_ = call.Hangup()
				sess.doCleanup(snd)
			}
		}
	}()

	<-sess.done
}

// releaseSession lepas session dari sender & bersihkan state — dipakai jalur
// error playcall SEBELUM call beneran mulai. Idempotent: aman dipanggil berulang.
func releaseSession(snd *Sender, sess *callSession) {
	snd.mu.Lock()
	if snd.sess == sess {
		snd.sess = nil
	}
	snd.mu.Unlock()
	sess.mu.Lock()
	sess.reset()
	sess.mu.Unlock()
	sess.stopAll()
}

// findSessionByTarget cari session aktif yang lagi nelpon nomor target ini.
func findSessionByTarget(cleanTarget string) *callSession {
	for _, s := range pool.activeSessions() {
		s.mu.Lock()
		match := s.target == cleanTarget
		s.mu.Unlock()
		if match {
			return s
		}
	}
	return nil
}

// addSongToSession download lagu lalu tambah ke antrian sess. Fix bug utama:
// kalau player lagi idle (lagu habis, antrian sempat kosong), langsung trigger
// playNextFn supaya lagu baru langsung bunyi — tidak perlu nunggu OnFinish lagi.
func addSongToSession(ctx context.Context, evt *events.Message, sess *callSession, title string) {
	chat := evt.Info.Chat

	track, err := fetchPlayTrack(title)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ lagu gak ketemu: "+err.Error())
		return
	}
	file, err := downloadAudio(track.DownloadURL)
	if err != nil || validateMP3(file) != nil {
		os.Remove(file)
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ gagal download/audio corrupt.")
		return
	}

	sess.mu.Lock()
	// Fix: call bisa sudah selesai/cleanup saat download jalan — kalau queue tetap
	// di-append, file temp-nya bocor (tidak ada yang membersihkan). Cek dulu.
	if !sess.active {
		sess.mu.Unlock()
		os.Remove(file)
		sendText(ctx, chat, "📭 Call sudah berakhir, lagu dibatalkan.")
		return
	}
	sess.queue = append(sess.queue, queueItem{track: track, file: file, evt: evt})
	pos := len(sess.queue)
	name := sess.sender.name
	shortID := sess.shortID
	// Cek apakah player lagi idle — kalau iya, kita perlu trigger manual.
	// Kondisi ini terjadi saat lagu sebelumnya sudah habis & antrian waktu itu kosong
	// (OnFinish sudah fire, "semua lagu habis" sudah dikirim), sehingga tidak ada
	// trigger otomatis lagi untuk memulai lagu baru.
	playerIdle := sess.player != nil && sess.player.State() == meowcaller.PlayerIdle
	fn := sess.playNext
	sess.mu.Unlock()

	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("🎵 *%s* - %s\n📋 Antrian #%d | 🤖 %s | 🔖 `%s`",
		track.Title, track.Artist, pos, name, shortID))

	// Trigger langsung kalau player idle — fix bug "lagu masuk antrian tapi gak bunyi".
	if playerIdle && fn != nil {
		fn()
	}
}

// handleContinue: trigger manual playNextFn kalau player stuck di idle padahal
// antrian masih ada. Alias: m!continue [sender] atau m!cnt [sender].
func handleContinue(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	sess, msg := pool.sessionFor(strings.TrimSpace(args))
	if sess == nil {
		sendText(ctx, chat, msg)
		return
	}

	sess.mu.Lock()
	hasQueue := len(sess.queue) > 0
	playerIdle := sess.player != nil && sess.player.State() == meowcaller.PlayerIdle
	fn := sess.playNext
	sess.mu.Unlock()

	if !hasQueue {
		sendText(ctx, chat, fmt.Sprintf(
			"📭 Antrian kosong.\n➕ Tambah lagu: *%scontinue <judul>*", Prefix))
		return
	}
	if !playerIdle {
		sendText(ctx, chat, "▶️ Lagu sedang diputar, tidak perlu dilanjutkan.")
		return
	}
	if fn == nil {
		sendText(ctx, chat, "❌ Call belum siap.")
		return
	}
	reactMsg(ctx, evt, "▶️")
	safeGo(fn)
}

// ─── Send / React ─────────────────────────────────────────────────────────────

// sendText — kirim teks "rich": pakai ExtendedTextMessage supaya formatting
// markdown WhatsApp dirender klien (*bold*, _italic_, ~strike~, `code`).
// Sebelumnya Conversation polos → asterisk tampil mentah di semua pesan bot.
// Hampir semua pesan bot juga tampil seperti "diteruskan dari saluran"
// (badge saluran via newsletterCtxInfo) — konsisten dengan menu. Kalau ctx
// saluran tidak tersedia (fetch gagal), pesan tetap terkirim normal.
func sendText(ctx context.Context, to types.JID, text string) {
	sendTextWithCtx(ctx, to, text, newsletterCtxInfo(ctx))
}

// msgContextInfo ambil ContextInfo dari tipe pesan APA PUN — bukan cuma
// extendedText. Klik tombol menu (interactiveResponse) dan caption media
// juga membawa ContextInfo (reply/tag); dulu hanya extendedText yang dibaca
// sehingga reply/tag "hilang" untuk command yang masuk lewat jalur itu
// (terlihat sebagai "!kick: reply pesan target" padahal user sudah reply).
// Getter protobuf nil-safe, jadi aman dipanggil berantai.
func msgContextInfo(evt *events.Message) *waE2E.ContextInfo {
	if evt == nil || evt.Message == nil {
		return nil
	}
	m := evt.Message
	for _, ci := range []*waE2E.ContextInfo{
		m.GetExtendedTextMessage().GetContextInfo(),
		m.GetImageMessage().GetContextInfo(),
		m.GetVideoMessage().GetContextInfo(),
		m.GetDocumentMessage().GetContextInfo(),
		m.GetInteractiveResponseMessage().GetContextInfo(),
	} {
		if ci != nil {
			return ci
		}
	}
	return nil
}

// ─── Reply ke pesan command ──────────────────────────────────────────────────

// ctxKeyCmdEvt — key context untuk events.Message command yang sedang diproses.
type ctxKeyCmdEvt struct{}

// withCommandEvt tandai ctx dengan evt command, supaya sendText/media otomatis
// jadi REPLY ke pesan perintah user (bukan pesan terpisah). Dipanggil di
// handleMessage sebelum dispatch; ctx lain (background, channel) tidak punya
// evt → pesannya tetap pesan biasa.
func withCommandEvt(ctx context.Context, evt *events.Message) context.Context {
	return context.WithValue(ctx, ctxKeyCmdEvt{}, evt)
}

// quotedCtxInfo bangun ContextInfo quote ke pesan command dari ctx.
// Nil kalau ctx bukan berasal dari command.
func quotedCtxInfo(ctx context.Context) *waE2E.ContextInfo {
	evt, ok := ctx.Value(ctxKeyCmdEvt{}).(*events.Message)
	if !ok || evt == nil {
		return nil
	}
	ci := &waE2E.ContextInfo{
		StanzaID:      proto.String(evt.Info.ID),
		QuotedMessage: evt.Message,
	}
	if evt.Info.IsGroup {
		ci.Participant = proto.String(evt.Info.Sender.ToNonAD().String())
	} else {
		ci.Participant = proto.String(evt.Info.Chat.String())
	}
	return ci
}

// mergeReplyCtx tempelkan quoted command ke ContextInfo (dipakai media).
// Kalau ctx bukan dari command, ci dikembalikan apa adanya.
func mergeReplyCtx(ctx context.Context, ci *waE2E.ContextInfo) *waE2E.ContextInfo {
	q := quotedCtxInfo(ctx)
	if q == nil {
		return ci
	}
	if ci == nil {
		ci = &waE2E.ContextInfo{}
	}
	ci.StanzaID = q.StanzaID
	ci.Participant = q.Participant
	ci.QuotedMessage = q.QuotedMessage
	return ci
}

// sendTextWithCtx — sendText plus ContextInfo opsional. Dipakai saat pesan
// mewakili konten saluran (mis. hasil !fetchch) biar tampil seperti
// "diteruskan dari saluran" via newsletterCtxInfo.
func sendTextWithCtx(ctx context.Context, to types.JID, text string, ci *waE2E.ContextInfo) {
	// Reply ke pesan command user (kalau ctx berasal dari command).
	if q := quotedCtxInfo(ctx); q != nil {
		if ci == nil {
			ci = &waE2E.ContextInfo{}
		}
		ci.StanzaID = q.StanzaID
		ci.Participant = q.Participant
		ci.QuotedMessage = q.QuotedMessage
	}
	_, err := waClient.SendMessage(ctx, to, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: ci,
		},
	})
	if err != nil {
		pool.logger.Error().Err(err).Str("to", to.String()).Msg("gagal kirim pesan")
	}
}

func reactMsg(ctx context.Context, evt *events.Message, emoji string) {
	_, err := waClient.SendMessage(ctx, evt.Info.Chat, &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(evt.Info.Chat.String()),
				FromMe:      proto.Bool(evt.Info.IsFromMe),
				ID:          proto.String(evt.Info.ID),
				Participant: proto.String(evt.Info.Sender.String()),
			},
			Text:              proto.String(emoji),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	})
	if err != nil {
		pool.logger.Error().Err(err).Str("emoji", emoji).Msg("gagal kirim reaksi")
	}
}
