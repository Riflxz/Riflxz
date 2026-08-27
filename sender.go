package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Sender = satu akun WhatsApp yang bisa dipakai buat NELPON. Bot punya banyak sender
// (multi-device): kalau sender 1 lagi dipakai nelpon orang, .playcall berikutnya
// otomatis pindah ke sender 2, dst — jadi bisa banyak call barengan.
//
// Sender pertama (index 0) juga jadi "bot utama": dia yang nangkep semua command &
// balesin chat. Sender lain khusus buat nelpon aja.
type Sender struct {
	name   string // "sender1", "sender2", ...
	wa     *whatsmeow.Client
	call   *meowcaller.Client
	device *store.Device

	mu        sync.Mutex
	sess      *callSession  // audio call (.playcall) yang lagi jalan di sender ini (nil = nganggur)
	videoSess *videoSession // video call (.playvideo) yang lagi jalan (nil = nganggur)
}

// number balikin nomor telpon sender (buat ditampilin), atau "?" kalau belum login.
func (s *Sender) number() string {
	if s.wa != nil && s.wa.Store != nil && s.wa.Store.ID != nil {
		return s.wa.Store.ID.User
	}
	return "?"
}

func (s *Sender) connected() bool {
	return s.wa != nil && s.wa.IsConnected() && s.wa.IsLoggedIn()
}

// inCall balikin true kalau sender ini lagi sibuk — call audio (.playcall) ATAU
// video (.playvideo). Satu sumber kebenaran ini dipakai pool.acquireFree() buat
// dua-duanya, jadi otomatis gak saling nabrak: sender yang lagi playcall gak
// bakal kepilih buat playvideo, begitu juga sebaliknya.
func (s *Sender) inCall() bool {
	s.mu.Lock()
	sess, vsess := s.sess, s.videoSess
	s.mu.Unlock()
	if sess != nil {
		// Fix data race: sess.active dilindungi sess.mu, bukan s.mu —
		// baca di sini dengan lock yang benar (pola Fix CM-01).
		sess.mu.Lock()
		active := sess.active
		sess.mu.Unlock()
		if active {
			return true
		}
	}
	if vsess != nil {
		vsess.mu.Lock()
		active := vsess.active
		vsess.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}

// videoSession balikin video session sender ini, bikin baru kalau belum ada.
func (s *Sender) videoSession() *videoSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.videoSess == nil {
		s.videoSess = &videoSession{sender: s}
	}
	return s.videoSess
}

// ─── Pool ─────────────────────────────────────────────────────────────────────

type senderPool struct {
	mu        sync.Mutex
	senders   []*Sender
	container *sqlstore.Container
	logger    zerolog.Logger
}

var pool = &senderPool{}

// initFromDevices bikin Sender buat tiap device yang udah login di DB. Device pertama
// jadi bot utama. Client-nya di-connect + event handler dipasang di sini.
func (p *senderPool) initFromDevices(ctx context.Context, devices []*store.Device) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, dev := range devices {
		s := p.buildSender(i, dev)
		p.senders = append(p.senders, s)
	}
}

// buildSender bikin satu Sender dari device (belum di-connect). Caller connect sendiri.
func (p *senderPool) buildSender(index int, dev *store.Device) *Sender {
	name := fmt.Sprintf("sender%d", index+1)
	// libLog: library internal hanya tampil kalau Error ke atas.
	// Warning dari whatsmeow (history sync 400, dll) adalah noise internal WA
	// yang tidak bisa difix — lifecycle penting sudah dicatat oleh event handler kita.
	libLog := p.logger.Level(zerolog.ErrorLevel)
	wa := whatsmeow.NewClient(dev, waLog.Zerolog(libLog).Sub(name))
	call := meowcaller.NewClient(wa, meowcaller.WithLogger(libLog))
	s := &Sender{name: name, wa: wa, call: call, device: dev}

	// Cuma sender utama (index 0) yang nangkep command & event chat.
	if index == 0 {
		wa.AddEventHandler(func(evt any) { mainSenderEvents(s, evt) })
	}
	return s
}

// connectAll nyambungin semua sender ke WhatsApp.
func (p *senderPool) connectAll() {
	p.mu.Lock()
	senders := append([]*Sender(nil), p.senders...)
	p.mu.Unlock()
	for _, s := range senders {
		if err := s.wa.Connect(); err != nil {
			p.logger.Warn().Err(err).Str("sender", s.name).Msg("gagal connect sender")
		}
	}
}

// main balikin sender utama (index 0), atau nil kalau pool kosong.
func (p *senderPool) main() *Sender {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.senders) == 0 {
		return nil
	}
	return p.senders[0]
}

// list balikin salinan daftar sender (buat listsender).
func (p *senderPool) list() []*Sender {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*Sender(nil), p.senders...)
}

// acquireFree cari sender yang connected & lagi nganggur (gak lagi call). Balikin nil
// kalau semua sibuk / gak ada yang online.
func (p *senderPool) acquireFree() *Sender {
	p.mu.Lock()
	senders := append([]*Sender(nil), p.senders...)
	p.mu.Unlock()
	for _, s := range senders {
		if s.connected() && !s.inCall() {
			return s
		}
	}
	return nil
}

// acquireReserveAudio cari sender nganggur DAN langsung reserve (s.sess = sess)
// secara atomik di dalam s.mu. Fix TOCTOU: pola lama (acquireFree lalu set
// snd.sess manual) punya celah — dua .playcall konkuren bisa dapat sender yang
// SAMA (cek & reserve tidak atomik), yang kedua menimpa yang pertama.
func (p *senderPool) acquireReserveAudio(sess *callSession) *Sender {
	p.mu.Lock()
	senders := append([]*Sender(nil), p.senders...)
	p.mu.Unlock()
	for _, s := range senders {
		s.mu.Lock()
		if s.connected() && s.sess == nil && !s.videoBusy() {
			s.sess = sess
			sess.sender = s
			s.mu.Unlock()
			return s
		}
		s.mu.Unlock()
	}
	return nil
}

// acquireReserveVideo cari sender nganggur DAN langsung reserve slot video
// (active=true, gen++) secara atomik. Fix TOCTOU yang sama dengan audio.
// Balikin (sender, videoSession) — gen sudah naik, caller tinggal isi sisanya.
func (p *senderPool) acquireReserveVideo() (*Sender, *videoSession) {
	p.mu.Lock()
	senders := append([]*Sender(nil), p.senders...)
	p.mu.Unlock()
	for _, s := range senders {
		s.mu.Lock()
		if !s.connected() || s.sess != nil {
			s.mu.Unlock()
			continue
		}
		vs := s.videoSess
		if vs == nil {
			vs = &videoSession{sender: s}
			s.videoSess = vs
		}
		vs.mu.Lock()
		if vs.active {
			vs.mu.Unlock()
			s.mu.Unlock()
			continue
		}
		vs.gen++
		vs.active = true
		vs.mu.Unlock()
		s.mu.Unlock()
		return s, vs
	}
	return nil, nil
}

// videoBusy cek apakah slot video sender ini lagi dipakai (dipanggil dengan
// s.mu sudah dipegang oleh caller).
func (s *Sender) videoBusy() bool {
	vs := s.videoSess
	if vs == nil {
		return false
	}
	vs.mu.Lock()
	active := vs.active
	vs.mu.Unlock()
	return active
}

// safeGo jalankan fn di goroutine dengan recover — panic di handler (nil
// pointer, type assertion, dll) tidak boleh mematikan seluruh bot. Semua
// goroutine yang di-spawn dari router WAJIB lewat sini.
func safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				pool.logger.Error().Interface("panic", r).Msg("recovered panic di goroutine")
			}
		}()
		fn()
	}()
}

// byName cari sender by nama ("sender1", "sender2"), case-insensitive. nil kalau gak ada.
func (p *senderPool) byName(name string) *Sender {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.senders {
		if strings.EqualFold(s.name, name) {
			return s
		}
	}
	return nil
}

// activeSessions balikin semua session yang lagi call (dari semua sender).
// Fix CM-01: sess.active harus dibaca dengan sess.mu (bukan s.mu) untuk
// menghindari data race — dua lock berbeda melindungi field yang sama.
func (p *senderPool) activeSessions() []*callSession {
	p.mu.Lock()
	senders := append([]*Sender(nil), p.senders...)
	p.mu.Unlock()
	var out []*callSession
	for _, s := range senders {
		s.mu.Lock()
		sess := s.sess
		s.mu.Unlock()
		if sess == nil {
			continue
		}
		// Baca active dengan lock yang benar: sess.mu
		sess.mu.Lock()
		active := sess.active
		sess.mu.Unlock()
		if active {
			out = append(out, sess)
		}
	}
	return out
}

// sessionFor cari session aktif buat sebuah command. Kalau senderName diisi, cari sender
// itu. Kalau kosong & cuma ADA 1 call aktif → pakai itu. Kalau banyak → nil + minta
// disebutin. Balikin (session, errorMsg).
func (p *senderPool) sessionFor(senderName string) (*callSession, string) {
	if senderName != "" {
		s := p.byName(senderName)
		if s == nil {
			return nil, "❌ Sender *" + senderName + "* gak ada. Cek *" + Prefix + "listsender*."
		}
		s.mu.Lock()
		sess := s.sess
		s.mu.Unlock()
		if sess == nil {
			return nil, "📭 *" + senderName + "* lagi gak ada call."
		}
		// Fix CM-01 (terlewat di cabang ini): sess.active dilindungi sess.mu,
		// bukan s.mu — baca dengan lock yang benar.
		sess.mu.Lock()
		active := sess.active
		sess.mu.Unlock()
		if !active {
			return nil, "📭 *" + senderName + "* lagi gak ada call."
		}
		return sess, ""
	}
	act := p.activeSessions()
	switch len(act) {
	case 0:
		return nil, "📭 Gak ada call aktif."
	case 1:
		return act[0], ""
	default:
		return nil, fmt.Sprintf("❓ Ada %d call aktif. Sebutin sender-nya, contoh: *%sskip sender1*", len(act), Prefix)
	}
}

// videoSessionFor: sama kayak sessionFor tapi buat video call. Sender bisa punya
// call audio & video BARENGAN (dua slot beda), makanya lookup-nya terpisah.
func (p *senderPool) videoSessionFor(senderName string) (*videoSession, string) {
	if senderName != "" {
		s := p.byName(senderName)
		if s == nil {
			return nil, "❌ Sender *" + senderName + "* gak ada. Cek *" + Prefix + "listsender*."
		}
		s.mu.Lock()
		vs := s.videoSess
		s.mu.Unlock()
		if vs == nil || !vs.isActive() {
			return nil, "📭 *" + senderName + "* lagi gak ada video call."
		}
		return vs, ""
	}
	var active []*videoSession
	for _, s := range p.list() {
		s.mu.Lock()
		vs := s.videoSess
		s.mu.Unlock()
		if vs != nil && vs.isActive() {
			active = append(active, vs)
		}
	}
	switch len(active) {
	case 0:
		return nil, "📭 Gak ada video call aktif."
	case 1:
		return active[0], ""
	default:
		return nil, fmt.Sprintf("❓ Ada %d video call aktif. Sebutin sender-nya, contoh: *%sskipvideo sender1*", len(active), Prefix)
	}
}

// addSender bikin device baru, mulai pairing pakai nomor telpon, balikin pairing code.
// Sender-nya langsung dimasukin pool (status connecting sampai PairSuccess).
func (p *senderPool) addSender(ctx context.Context, phone string) (code string, name string, err error) {
	dev := p.container.NewDevice()
	p.mu.Lock()
	index := len(p.senders)
	s := p.buildSender(index, dev)
	p.senders = append(p.senders, s)
	p.mu.Unlock()

	if err := s.wa.Connect(); err != nil {
		return "", "", fmt.Errorf("connect: %w", err)
	}

	// Pasang handler sementara buat tau pairing sukses.
	// Fix GL-04: simpan handler ID lalu remove setelah Connected pertama
	// agar handler tidak terakumulasi jika addSender dipanggil berkali-kali.
	var tempHandlerID uint32
	tempHandlerID = s.wa.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.PairSuccess:
			p.logger.Info().Str("sender", s.name).Str("num", s.number()).Msg("sender pairing sukses")
		case *events.Connected:
			_ = s.wa.SendPresence(context.Background(), types.PresenceAvailable)
			s.wa.RemoveEventHandler(tempHandlerID) // self-remove setelah connected
		}
	})

	ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, err = s.wa.PairPhone(ctx2, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", "", fmt.Errorf("pair: %w", err)
	}
	return code, s.name, nil
}

// removeSender: disconnect & buang sender dari pool. Dipake tombol "Batalkan"
// di .addsender (pairing belum kelar / salah nomor).
func (p *senderPool) removeSender(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.senders {
		if s.name == name {
			if s.wa != nil {
				s.wa.Disconnect()
			}
			p.senders = append(p.senders[:i], p.senders[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("sender %s gak ketemu", name)
}

// ─── Auto-read (!read on/off) ────────────────────────────────────────────────
// Port plugin JS: `if (m.message) { await this.readMessages([m.key]) }` — bot
// langsung menandai pesan masuk sebagai sudah dibaca. Toggle via !read on/off
// (alias !autoread), default OFF.

var (
	autoReadMu sync.RWMutex
	autoRead   bool
)

func getAutoRead() bool {
	autoReadMu.RLock()
	defer autoReadMu.RUnlock()
	return autoRead
}

func setAutoRead(b bool) {
	autoReadMu.Lock()
	autoRead = b
	autoReadMu.Unlock()
}

// handleRead — !read on / off / (tanpa arg → status).
func handleRead(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "on", "1", "true", "yes", "aktif":
		setAutoRead(true)
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "📖 Auto-read: *ON* — pesan masuk otomatis ditandai sudah dibaca")
		pool.logger.Info().Msg("auto-read ON")
	case "off", "0", "false", "no", "mati":
		setAutoRead(false)
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "📖 Auto-read: *OFF*")
		pool.logger.Info().Msg("auto-read OFF")
	default:
		st := "OFF"
		if getAutoRead() {
			st = "ON"
		}
		sendText(ctx, chat, fmt.Sprintf(
			"📖 Auto-read sekarang: *%s*\n\n`%sread on` — nyalakan\n`%sread off` — matikan",
			st, Prefix, Prefix))
	}
}

// mainSenderEvents nangkep event di sender utama: pesan masuk (→ router command) &
// lifecycle. Cuma sender utama yang punya handler ini.
func mainSenderEvents(s *Sender, evt any) {
	// Fix: pakai appCtx (signal ctx dari main) — shutdown bisa membatalkan
	// work in-flight; dulu context.Background() yang tidak pernah di-cancel.
	ctx := appCtx
	switch v := evt.(type) {
	case *events.Message:
		if getAutoRead() && !v.Info.IsFromMe && v.Info.Chat.Server != "newsletter" {
			// Port JS readMessages([m.key]) tanpa delay(0) — MarkRead sinkron
			// & cepat; error diabaikan (read cuma bonus, bukan fungsional).
			_ = waClient.MarkRead(ctx, []types.MessageID{v.Info.ID}, v.Info.Timestamp, v.Info.Chat, v.Info.Sender)
		}
		// Debug capture: log semua pesan dari channel (newsletter) lengkap dengan
		// protobuf-nya. Dipakai buat membedah bentuk asli pesan status channel
		// (NewsletterAdminProfileStatusMessage dll) yang dikirim server saat
		// status diubah manual lewat aplikasi resmi — biar bisa ditiru persis.
		// ⚠️ DISABLED sementara — dulu dump juga dikirim ke chat owner (spam).
		// Aktifkan lagi kalau riset status channel dilanjut.
		handleMessage(ctx, v)
	case *events.GroupInfo:
		// Fitur jaga grup: sambut member yang baru join (config per grup).
		handleGroupJoin(ctx, v)
	case *events.Connected:
		pool.logger.Info().Str("sender", s.name).Msg("✅ bot utama connected")
		_ = s.wa.SendPresence(ctx, types.PresenceAvailable)
		// Auto-JPM: lanjutkan jadwal yang tersimpan setelah restart/koneksi ulang.
		startAutoJpmSchedulerIfEnabled()
		// Sinkronisasi state runtime (sekali per proses).
		restoreSessionState(ctx)
	case *events.LoggedOut:
		pool.logger.Warn().Msg("⚠️ bot utama logged out — hapus yuukibot.db & login ulang")
	}
}
