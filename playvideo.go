package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types/events"
)

// videoSession = satu video call, nempel ke SATU sender (lihat Sender.videoSess).
// `gen` naik tiap sesi baru; `stop`/`closeStop` per-sesi. Aset (accessUnits+audioPath)
// disiapin DULUAN (download+encode) sebelum call dilempar, jadi pas peer angkat video
// langsung siap streaming — gak ada delay.
type videoSession struct {
	mu          sync.Mutex
	sender      *Sender
	active      bool
	target      string
	requester   string // user yang manggil .playvideo — dipake buat validasi tap tombol
	call        *meowcaller.Call
	gen         uint64
	stop        chan struct{}
	closeStop   func()
	accessUnits [][]byte
	audioPath   string
	title       string
	// streamStarted: guard manual pengganti sync.Once — streamOnce di-reset per
	// sesi (vs.streamOnce = sync.Once{}) sambil bisa dipakai OnReady sesi lama
	// → data race pada field sync.Once. Semua akses di bawah vs.mu.
	streamStarted bool
}

func (v *videoSession) isActive() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

// endSession clear state sesi (cuma kalau gen cocok & masih active) & hapus file
// audio. Fix: gate dengan !v.active — sebelumnya OnEnd + handler manual bisa
// memanggil ini 2x dengan gen sama (gen tidak di-reset), sehingga
// advanceVideoQueue() jalan 2x dan dua video antrian ter-pop sekaligus.
func (v *videoSession) endSession(gen uint64) {
	v.mu.Lock()
	if v.gen != gen || !v.active {
		v.mu.Unlock()
		return
	}
	p := v.audioPath
	v.active = false
	v.target = ""
	v.call = nil
	v.accessUnits = nil
	v.audioPath = ""
	v.title = ""
	v.mu.Unlock()
	if p != "" {
		os.Remove(p)
	}
	go advanceVideoQueue() // otomatis lanjut ke antrian berikutnya (kalau ada)
}

// mine cek apakah sesi ini (gen) masih yang aktif.
func (v *videoSession) mine(gen uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active && v.gen == gen
}

// videoQueueItem = 1 permintaan .playvideo yang nunggu SEMUA sender sibuk.
// Begitu ada sender nganggur (dari mana pun — call biasa atau video kelar),
// antrian ini otomatis dicoba lagi lewat advanceVideoQueue.
type videoQueueItem struct {
	ctx  context.Context
	evt  *events.Message
	args string
}

var (
	videoQueueMu   sync.Mutex
	videoQueueList []videoQueueItem
)

// advanceVideoQueue ambil item paling depan (kalau ada) & coba mulai lagi.
// Kalau semua sender masih sibuk, otomatis balik masuk antrian lagi lewat
// handlePlayVideo (gak ilang, cuma nunggu giliran berikutnya).
func advanceVideoQueue() {
	videoQueueMu.Lock()
	if len(videoQueueList) == 0 {
		videoQueueMu.Unlock()
		return
	}
	next := videoQueueList[0]
	videoQueueList = videoQueueList[1:]
	videoQueueMu.Unlock()
	handlePlayVideo(next.ctx, next.evt, next.args)
}

// handleStopVideo: hangup TOTAL video call di sender tsb — bersihin antrian juga
// (gak lanjut ke video berikutnya). senderArg boleh kosong kalau cuma 1 yang aktif.
func handleStopVideo(ctx context.Context, evt *events.Message, senderArg string) {
	vs, msg := pool.videoSessionFor(strings.TrimSpace(senderArg))
	videoQueueMu.Lock()
	cleared := len(videoQueueList)
	videoQueueList = nil
	videoQueueMu.Unlock()

	if vs == nil {
		if cleared > 0 {
			sendText(ctx, evt.Info.Chat, fmt.Sprintf("%s Antrian dibersihin (%d item).", msg, cleared))
		} else {
			sendText(ctx, evt.Info.Chat, msg)
		}
		return
	}

	vs.mu.Lock()
	call := vs.call
	closeStop := vs.closeStop
	gen := vs.gen
	vs.mu.Unlock()

	reactMsg(ctx, evt, "🛑")
	if cleared > 0 {
		sendText(ctx, evt.Info.Chat, fmt.Sprintf("🛑 Video call dihangup. Antrian dibersihin (%d item).", cleared))
	} else {
		sendText(ctx, evt.Info.Chat, "🛑 Video call dihangup.")
	}
	if call != nil {
		_ = call.Hangup() // → OnEnd → closeStop + endSession (antrian udah kosong, gak lanjut)
	}
	// Fix: kalau Hangup error (atau OnEnd tidak sempat terdaftar karena race),
	// closeStop + endSession harus tetap jalan — keduanya idempotent
	// (stopOnce untuk close, gate active untuk endSession), aman dipanggil
	// walau OnEnd juga memanggilnya.
	if closeStop != nil {
		closeStop()
	}
	vs.endSession(gen)
}

// handleSkipVideo: hangup video yang lagi jalan di 1 sender, antrian TETEP jalan
// (otomatis lanjut lewat endSession -> advanceVideoQueue).
func handleSkipVideo(ctx context.Context, evt *events.Message, senderArg string) {
	vs, msg := pool.videoSessionFor(strings.TrimSpace(senderArg))
	if vs == nil {
		sendText(ctx, evt.Info.Chat, msg)
		return
	}

	vs.mu.Lock()
	call := vs.call
	closeStop := vs.closeStop
	gen := vs.gen
	vs.mu.Unlock()

	videoQueueMu.Lock()
	hasNext := len(videoQueueList) > 0
	videoQueueMu.Unlock()

	reactMsg(ctx, evt, "⏭️")
	if hasNext {
		sendText(ctx, evt.Info.Chat, "⏭️ Skip, lanjut ke video berikutnya di antrian...")
	} else {
		sendText(ctx, evt.Info.Chat, "⏭️ Skip. Antrian kosong, video call diakhirin.")
	}
	if call != nil {
		_ = call.Hangup()
	}
	// Fix: sama seperti handleStopVideo — closeStop + endSession harus tetap
	// jalan walau Hangup error / OnEnd tidak fire (idempotent, aman dobel).
	if closeStop != nil {
		closeStop()
	}
	vs.endSession(gen)
}

// handleVideoQueue: nampilin semua video call yang lagi jalan (semua sender) +
// antrian yang lagi nunggu giliran. Fix PRIVACY: nomor target cuma ditampilkan
// ke owner (di public mode command ini bisa dipakai semua orang).
func handleVideoQueue(ctx context.Context, evt *events.Message, ownerSender bool) {
	var b strings.Builder

	anyActive := false
	for _, s := range pool.list() {
		s.mu.Lock()
		vs := s.videoSess
		s.mu.Unlock()
		if vs == nil {
			continue
		}
		vs.mu.Lock()
		active, title, target := vs.active, vs.title, vs.target
		vs.mu.Unlock()
		if active {
			anyActive = true
			if ownerSender {
				fmt.Fprintf(&b, "🎬 *%s*: %s → +%s\n", s.name, title, target)
			} else {
				fmt.Fprintf(&b, "🎬 *%s*: %s\n", s.name, title)
			}
		}
	}
	if !anyActive {
		fmt.Fprintf(&b, "📭 Gak ada video yang lagi jalan.\n")
	}
	b.WriteString("\n")

	videoQueueMu.Lock()
	items := append([]videoQueueItem(nil), videoQueueList...)
	videoQueueMu.Unlock()

	if len(items) == 0 {
		fmt.Fprintf(&b, "📜 *Antrian video:* _kosong_")
	} else {
		fmt.Fprintf(&b, "📜 *Antrian video:*\n")
		for i, it := range items {
			fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(it.args))
		}
	}
	sendText(ctx, evt.Info.Chat, b.String())
}

func handlePlayVideo(ctx context.Context, evt *events.Message, args string) {
	reactMsg(ctx, evt, "⏳")
	chat := evt.Info.Chat

	// Fix: cooldown anti-spam yang sama dengan .playcall — sebelumnya hanya
	// .playcall yang di-enforce, .playvideo bisa di-spam tanpa batas.
	if ok, wait := checkCooldown(senderUser(evt)); !ok {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("⏳ Cooldown, tunggu %ds lagi.", int(wait.Seconds())+1))
		return
	}

	if err := checkFFmpeg(); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}

	target, ytURL, err := resolveTarget(ctx, evt, args) // "judul" = LINK YouTube
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	cleanTarget := cleanJIDNumber(target)

	// Cari sender nganggur + reserve ATOMIK (Fix TOCTOU: cek & reserve dalam
	// satu lock — dua .playvideo/.playcall konkuren tidak bisa dapat sender
	// yang sama). Sama fungsi yang dipakai .playcall (Sender.inCall cek
	// audio+video), jadi otomatis gak nabrak satu sama lain.
	snd, vs := pool.acquireReserveVideo()
	if snd == nil {
		videoQueueMu.Lock()
		videoQueueList = append(videoQueueList, videoQueueItem{ctx: ctx, evt: evt, args: args})
		pos := len(videoQueueList)
		videoQueueMu.Unlock()
		reactMsg(ctx, evt, "📋")
		sendText(ctx, chat, fmt.Sprintf(
			"📋 Semua sender lagi sibuk. Ditambahin ke antrian (posisi #%d) — otomatis lanjut begitu ada yg nganggur.",
			pos,
		))
		return
	}
	myGen := vs.gen // sudah di-set oleh acquireReserveVideo

	// Reserve sesi dulu (active=true) biar sender ini gak kesamber .playcall/.playvideo
	// lain pas kita masih download/encode.
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }

	vs.mu.Lock()
	vs.target = cleanTarget
	vs.requester = senderUser(evt)
	vs.stop = stop
	vs.closeStop = closeStop
	vs.streamStarted = false
	vs.accessUnits = nil
	vs.audioPath = ""
	vs.title = ""
	vs.call = nil
	vs.mu.Unlock()

	// ── DOWNLOAD + ENCODE DULU (biar video siap sebelum nelpon) ──────────────────
	sendText(ctx, chat, fmt.Sprintf("⏳ Nyiapin video via %s (download + encode)...", snd.name))
	aus, audioPath, title, err := buildVideoAssets(ytURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ gagal siapin video: "+err.Error())
		vs.endSession(myGen)
		return
	}
	// Keburu di-stop pas lagi encode?
	if !vs.mine(myGen) {
		os.Remove(audioPath)
		return
	}
	vs.mu.Lock()
	vs.accessUnits = aus
	vs.audioPath = audioPath
	vs.title = title
	vs.mu.Unlock()

	sendText(ctx, chat, fmt.Sprintf(
		"🎬 *%s*\n📞 %s → *+%s*...\n📲 *Angkat WA-nya!* Video langsung muncul.",
		title, snd.name, cleanTarget,
	))

	// ── Baru nelpon (video udah siap) — pakai client SENDER ini, bukan bot utama ──
	var call *meowcaller.Call
	for attempt := 1; attempt <= 3; attempt++ {
		callCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		call, err = snd.call.CallVideo(callCtx, target)
		cancel()
		if err == nil {
			break
		}
		pool.logger.Warn().Str("sender", snd.name).Int("attempt", attempt).Err(err).Msg("playvideo attempt gagal")
		if attempt < 3 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ gagal nelpon +"+cleanTarget+": "+err.Error())
		vs.endSession(myGen)
		return
	}

	// Fix PHANTOM-CALL (pola sama seperti playcall): set vs.call SEGERA setelah
	// CallVideo sukses supaya !stopvideo yang datang di celah ini menemukan call
	// yang bisa dihangup. Lalu cek mine: kalau sesi sudah di-endSession (mis.
	// !stopvideo saat masih download/encode), call yang baru sukses ini tidak
	// punya pemilik — hangup manual, kalau tidak dia ring selamanya.
	vs.mu.Lock()
	vs.call = call
	stillMine := vs.active && vs.gen == myGen
	vs.mu.Unlock()
	if !stillMine {
		_ = call.Hangup()
		sendText(ctx, chat, "🛑 Video call dibatalkan sebelum tersambung.")
		return
	}

	go keepCallAlive(snd.wa, call.ID(), stop) // heartbeat dari akun sender yg bikin call ini

	connected := make(chan struct{}, 1)
	player := meowcaller.NewPlayer()
	call.Subscribe(player)

	// Audio Player cuma main SEKALI, video loop terus — OnFinish re-Play() pakai
	// source baru (source lama abis dibaca, gak bisa di-rewind) selama sesi aktif.
	var playAudioLoop func()
	playAudioLoop = func() {
		if !vs.mine(myGen) {
			return
		}
		src, e := meowcaller.MP3File(audioPath)
		if e != nil {
			sendText(ctx, chat, "❌ gagal load audio: "+e.Error())
			return
		}
		player.OnFinish(playAudioLoop)
		player.Play(src)
	}
	// startAudioOnce: OnVideoReady & OnReady DUA-DUANYA manggil ini (video pipeline
	// duluan biasanya, OnReady jaga-jaga kalau itu gak fire) — sync.Once mastiin
	// audio CUMA START SEKALI, gak restart dari 0 kalau kedua handler ke-trigger.
	var audioStartOnce sync.Once
	startAudioOnce := func() { audioStartOnce.Do(playAudioLoop) }

	// OnVideoReady: pipeline OUTBOUND siap kirim, tapi ini fire pas MASIH RINGING
	// (peer belum angkat) — sengaja GAK dipakai buat mulai video/audio di sini lagi.
	// Percobaan sebelumnya mulai video DI SINI bikin video "jalan diem-diem" selama
	// nge-ring (goroutine ticker custom kita gak peduli peer udah angkat apa belum),
	// sementara audio (di-pull sama call engine internal) baru beneran mulai progress
	// PAS connect asli — hasilnya video udah maju sekian detik (=lama nge-ring) duluan
	// pas audio baru mulai dari 0:00. Itu akar masalah "video duluan + loop di waktu
	// random" yang dilaporin. Gak dipakai lagi — biarin OnReady yang mulai DUA-DUANYA
	// di titik waktu yang SAMA PERSIS.
	call.OnVideoReady(func() {})

	// OnReady: fire pas ADA INBOUND dari peer (dia beneran angkat) — SATU-SATUNYA
	// titik video & audio mulai, bareng, biar clock start-nya identik.
	call.OnReady(func() {
		select {
		case connected <- struct{}{}:
		default:
		}
		reactMsg(ctx, evt, "✅")
		startVideoStream(vs, call, myGen, stop)
		startAudioOnce()
		sendText(ctx, chat, fmt.Sprintf(
			"✅ *Tersambung ke +%s*\n▶️ *%s*\n\n⏭️ Skip: *%sskipvideo %s* | 🛑 Stop: *%sstopvideo %s*",
			cleanTarget, title, Prefix, snd.name, Prefix, snd.name,
		))
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
		sendText(ctx, chat, fmt.Sprintf("📴 *Video Call Selesai* | %s", label))
		reactMsg(ctx, evt, "📴")
		closeStop()
		vs.endSession(myGen)
	})

	// Fix stuck-session (pola sama seperti playcall): kalau call sudah Ended DI
	// CELAH antara call diestablish dan OnEnd terdaftar, meowcaller tidak akan
	// pernah memanggil callback ini → stop tidak pernah di-close & sesi video
	// macet. Deteksi manual + cleanup idempotent (closeStop pakai stopOnce,
	// endSession di-gate active), jadi dobel-call tetap aman.
	if call.State() == meowcaller.CallPhaseEnded {
		pool.logger.Warn().Str("call_id", call.ID()).Msg("playvideo: call sudah ended sebelum OnEnd terdaftar, cleanup manual")
		sendText(ctx, chat, "📴 *Video Call ditutup*")
		closeStop()
		vs.endSession(myGen)
	}

	// Timeout 90 detik kalau gak diangkat.
	// Fix GL-01: time.NewTimer agar timer bisa di-Stop (tidak leak).
	// Fix: kalau Hangup gagal / OnEnd tidak fire, closeStop + endSession harus
	// tetap jalan — tanpa itu `<-stop` di bawah blok selamanya (goroutine bocor
	// & sender terkunci). Keduanya idempotent, aman dipanggil walau OnEnd juga.
	go func() {
		dialTimer := time.NewTimer(90 * time.Second)
		defer dialTimer.Stop()
		select {
		case <-connected:
		case <-stop:
		case <-dialTimer.C:
			sendText(ctx, chat, fmt.Sprintf("📵 *+%s* gak ngangkat (90s)", cleanTarget))
			_ = call.Hangup()
			closeStop()
			vs.endSession(myGen)
		}
	}()

	<-stop
}

// buildVideoAssets fetch link (theresav) → download mp4 → encode (h264 + mp3) → pecah
// jadi access unit. Balikin juga title.
func buildVideoAssets(ytURL string) (aus [][]byte, audioPath, title string, err error) {
	track, err := fetchVideoPlay(ytURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("video gak ke-resolve: %w", err)
	}
	title = track.Title

	mp4Path, err := downloadVideo(track.DownloadURL)
	if err != nil {
		return nil, "", title, fmt.Errorf("download: %w", err)
	}
	h264Path, audioPath, err := extractVideoAssets(mp4Path)
	os.Remove(mp4Path)
	if err != nil {
		return nil, "", title, err
	}
	aus, err = loadAccessUnits(h264Path)
	os.Remove(h264Path)
	if err != nil {
		os.Remove(audioPath)
		return nil, "", title, fmt.Errorf("parse H.264: %w", err)
	}

	// Diagnostic: video (dari jumlah frame) vs audio (ffprobe) durasinya beda gak?
	// Kalau beda >1 detik, itu penyebab audio/video "gak sinkron" pas sama-sama loop
	// independent (video loop by frame count, audio loop by OnFinish real EOF).
	videoDur := float64(len(aus)) / float64(videoFPS)
	if audioDur, perr := probeDuration(audioPath); perr == nil {
		diff := videoDur - audioDur
		if diff < 0 {
			diff = -diff
		}
		pool.logger.Debug().
			Float64("video_dur", videoDur).
			Int("frames", len(aus)).
			Int("fps", videoFPS).
			Float64("audio_dur", audioDur).
			Float64("selisih", diff).
			Msg("playvideo durasi encode")
		if diff > 1.0 {
			pool.logger.Warn().Float64("selisih", diff).Msg("playvideo: selisih durasi >1s, audio/video mungkin tidak sinkron")
		}
	}

	return aus, audioPath, title, nil
}

// startVideoStream mulai loop kirim frame video (sekali doang per sesi, dijaga
// streamStarted di bawah vs.mu — pengganti sync.Once yang di-reset per sesi dan
// rawan data race dengan OnReady sesi lama). Aset udah siap sebelum call, jadi
// langsung stream; loop dari IDR pertama sampai call/stop berakhir.
func startVideoStream(vs *videoSession, call *meowcaller.Call, gen uint64, stop <-chan struct{}) {
	vs.mu.Lock()
	if vs.streamStarted {
		vs.mu.Unlock()
		return
	}
	vs.streamStarted = true
	vs.mu.Unlock()
	go func() {
		if !vs.mine(gen) {
			return
		}
		vs.mu.Lock()
		aus := vs.accessUnits
		vs.mu.Unlock()
		if len(aus) == 0 {
			return
		}
		pool.logger.Info().Int("frames", len(aus)).Int("fps", videoFPS).Msg("playvideo: mulai stream")

		frameDur := time.Second / time.Duration(videoFPS)
		ticker := time.NewTicker(frameDur)
		defer ticker.Stop()
		idx := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			if idx >= len(aus) {
				idx = 0
			}
			au := aus[idx]
			idx++
			if err := call.SendVideo(au); err != nil {
				pool.logger.Warn().Err(err).Msg("playvideo: SendVideo berhenti")
				return
			}
		}
	}()
}
