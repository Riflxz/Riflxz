package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "modernc.org/sqlite"
)

// waClient & callClient = shortcut ke sender UTAMA (pool.senders[0]). Kode lama
// (playvideo, dll) masih pakai ini; playcall multi-sender pakai pool langsung.
var (
	waClient   *whatsmeow.Client
	callClient *meowcaller.Client
	startTime  = time.Now()
	// appCtx = context aplikasi (signal ctx dari main) — dipakai handler event
	// supaya work in-flight (call, download) bisa di-cancel saat shutdown.
	// Sebelumnya handler pakai context.Background() → shutdown tidak pernah
	// membatalkan apa pun.
	appCtx = context.Background()
)

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}).
		Level(zerolog.InfoLevel).
		With().Timestamp().Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Logger di context untuk LIBRARY pihak ketiga (dbutil/whatsmeow store)
	// dibatasi level Error — warning rutin seperti "Transaction took long"
	// (transaksi SQLite >1s, normal di WAL saat sync besar) tidak perlu
	// tampil. Log aplikasi sendiri tetap lewat pool.logger (level penuh).
	ctx = logger.Level(zerolog.ErrorLevel).WithContext(ctx)
	appCtx = ctx

	// libLog: library internal hanya tampil kalau Error ke atas.
	libLog := logger.Level(zerolog.ErrorLevel)

	container, err := sqlstore.New(
		ctx, "sqlite",
		"file:yuukibot.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		waLog.Zerolog(libLog).Sub("db"),
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("gagal buka session store (yuukibot.db)")
	}

	pool.container = container
	pool.logger = logger

	// Load SEMUA device yang udah login → jadi sender pool. Device pertama = bot utama.
	devices, err := container.GetAllDevices(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("gagal load devices dari session store")
	}

	// Flag login: --qr (login device baru via QR) & --pairing <nomor> (login
	// device baru via 8-digit pairing code, tanpa scan QR).
	qrMode := false
	pairNumber := ""
	for i := 1; i < len(os.Args); i++ {
		switch {
		case os.Args[i] == "--qr":
			qrMode = true
		case os.Args[i] == "--pairing" && i+1 < len(os.Args):
			pairNumber = os.Args[i+1]
		case strings.HasPrefix(os.Args[i], "--pairing="):
			pairNumber = strings.TrimPrefix(os.Args[i], "--pairing=")
		}
	}

	// Fallback ke .env (LOGIN_MODE / PAIRING_NUMBER) kalau flag CLI tidak diisi.
	// Flag CLI selalu menang. LOGIN_MODE=pairing tanpa PAIRING_NUMBER → pakai
	// BOT_NUMBER (env) atau BotNumber (config).
	if !qrMode && pairNumber == "" {
		switch cfgLoginMode() {
		case "pairing":
			pairNumber = cfgPairingNumber()
			if pairNumber == "" {
				pairNumber = envBotNumber()
			}
			if pairNumber == "" {
				pairNumber = BotNumber
			}
		case "qr":
			qrMode = true
		}
	}

	switch {
	case len(devices) == 0 && !qrMode && pairNumber == "":
		// Belum ada akun sama sekali → login bot utama via QR (perilaku lama).
		device := container.NewDevice()
		devices = []*store.Device{device}
		firstTimeQRLogin(ctx, logger, device)
	case qrMode || pairNumber != "":
		// Login device BARU (sender tambahan) via QR atau pairing code.
		device := container.NewDevice()
		if pairNumber != "" {
			pairingLogin(ctx, logger, device, pairNumber)
		} else {
			firstTimeQRLogin(ctx, logger, device)
		}
		devices = append(devices, device)
	}

	pool.initFromDevices(ctx, devices)

	// Shortcut ke sender utama buat kode lama (playvideo).
	mainS := pool.main()
	waClient = mainS.wa
	callClient = mainS.call

	// Connect semua sender.
	pool.connectAll()

	if err := waitUntilReady(ctx, waClient, 120*time.Second); err != nil {
		// Jangan Fatal — kalau timeout bot tetap jalan, koneksi mungkin
		// sudah siap tapi event-nya terlewat (race condition whatsmeow).
		logger.Warn().Err(err).Msg("⚠️ timeout nunggu koneksi — bot tetap jalan, coba kirim command")
	}
	if waClient.Store.PushName == "" {
		waClient.Store.PushName = BotName
	}

	senderCount := len(pool.list())
	logger.Info().
		Str("bot", BotName).
		Str("dev", BotDeveloper).
		Str("prefix", Prefix).
		Int("senders", senderCount).
		Msg("bot online")
	printBanner(senderCount)

	<-ctx.Done()
	for _, s := range pool.list() {
		s.wa.Disconnect()
	}
	// Fix: tutup session store — tanpa ini SQLite WAL tidak di-flush bersih
	// saat shutdown.
	if err := container.Close(); err != nil {
		logger.Warn().Err(err).Msg("gagal tutup session store")
	}
}

// visWidth hitung lebar tampil string (emoji = 2 kolom, variation selector = 0)
// supaya border banner rata di terminal.
func visWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == 0xFE0F || (r >= 0x1F3FB && r <= 0x1F3FF):
			// variation selector & skin tone: tidak menambah lebar
		case r >= 0x1F000 || (r >= 0x2190 && r <= 0x27BF):
			w += 2
		default:
			w++
		}
	}
	return w
}

// printBanner — banner startup rapi: semua baris dipad tepat ke lebar box,
// teks nilai sejajar vertikal (kolom key fix), tanpa baris kosong nyasar.
func printBanner(senders int) {
	const W = 36 // lebar dalam box (kolom visual; emoji dihitung 2)
	row := func(s string) {
		pad := W - visWidth(s)
		if pad < 0 {
			pad = 0
		}
		fmt.Printf("║%s%s║\n", s, strings.Repeat(" ", pad))
	}
	fmt.Println("╔" + strings.Repeat("═", W) + "╗")
	row("  🐱  " + BotName)
	row("  ⚡  by " + BotDeveloper)
	fmt.Println("╠" + strings.Repeat("═", W) + "╣")
	row(fmt.Sprintf("  🤖  %-7s: %d aktif", "Sender", senders))
	row(fmt.Sprintf("  📡  %-7s: %s", "Prefix", Prefix))
	row(fmt.Sprintf("  💡  %-7s: %smenu", "Panduan", Prefix))
	fmt.Println("╚" + strings.Repeat("═", W) + "╝")
	fmt.Println()
}

// firstTimeQRLogin nampilin QR buat login bot utama pertama kali. Client-nya di-connect
// di sini, di-disconnect abis pairing (nanti di-reconnect via pool.connectAll).
func firstTimeQRLogin(ctx context.Context, logger zerolog.Logger, device *store.Device) {
	cli := whatsmeow.NewClient(device, waLog.Zerolog(logger).Sub("qr"))
	qrChan, _ := cli.GetQRChannel(ctx)
	if err := cli.Connect(); err != nil {
		logger.Fatal().Err(err).Msg("gagal connect buat QR login")
	}
	for evt := range qrChan {
		if evt.Event == "code" {
			fmt.Println("\n========================================")
			fmt.Println(" SCAN QR INI DI WHATSAPP > LINKED DEVICES ")
			fmt.Println("========================================")
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			fmt.Printf("Kode valid %d detik, scan sebelum habis.\n\n", int(evt.Timeout.Seconds()))
		} else {
			logger.Info().Str("event", evt.Event).Msg("login event")
			if evt.Event == "success" {
				break
			}
		}
	}
	cli.Disconnect()
	time.Sleep(5 * time.Second) // kasih waktu store ke-flush & WA server siap reconnect
}

// pairingLogin login device baru pakai 8-digit pairing code — tanpa scan QR.
// Mirip firstTimeQRLogin: connect → pairing → disconnect (nanti di-reconnect
// via pool.connectAll). Nomor harus format internasional tanpa "+" (628xxx).
func pairingLogin(ctx context.Context, logger zerolog.Logger, device *store.Device, phone string) {
	cli := whatsmeow.NewClient(device, waLog.Zerolog(logger).Sub("pair"))
	qrChan, _ := cli.GetQRChannel(ctx)
	if err := cli.Connect(); err != nil {
		logger.Fatal().Err(err).Msg("gagal connect buat pairing login")
	}
	// Tunggu event QR pertama — tanda websocket beneran siap (per doc PairPhone:
	// "wait for the first item in the channel before calling PairPhone").
	select {
	case <-qrChan:
	case <-time.After(15 * time.Second):
		logger.Fatal().Msg("timeout nunggu koneksi buat pairing")
	}

	code, err := cli.PairPhone(ctx, phone, false, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		logger.Fatal().Err(err).Msg("gagal generate pairing code")
	}

	fmt.Printf("\n"+
		"========================================\n"+
		"  KODE PAIRING: %s\n"+
		"========================================\n"+
		"  WhatsApp > Linked Devices >\n"+
		"  Pair with phone number\n\n"+
		"  Kode valid ±160 detik.\n\n", code)

	deadline := time.Now().Add(160 * time.Second)
	for !cli.IsLoggedIn() {
		if time.Now().After(deadline) {
			logger.Fatal().Msg("timeout nunggu pairing dikonfirmasi di HP")
		}
		time.Sleep(time.Second)
	}
	logger.Info().Msg("✅ pairing sukses — perangkat terhubung")
	cli.Disconnect()
	time.Sleep(5 * time.Second) // kasih waktu store ke-flush & WA server siap reconnect
}

// waitUntilReady nunggu sampai client beneran connected + logged in.
// Pakai dua mekanisme sekaligus: event listener + polling ticker 500ms.
// Ini mencegah race condition dimana Connected event fire sebelum handler dipasang.
func waitUntilReady(ctx context.Context, client *whatsmeow.Client, timeout time.Duration) error {
	// Cek langsung dulu — kalau udah ready, langsung balik.
	if client.IsConnected() && client.IsLoggedIn() {
		return nil
	}

	ready := make(chan struct{}, 8)
	id := client.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	defer client.RemoveEventHandler(id)

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(timeout)

	for {
		select {
		case <-ready:
		case <-tick.C:
		case <-deadline:
			return fmt.Errorf("timed out waiting for connection")
		case <-ctx.Done():
			return ctx.Err()
		}
		if client.IsConnected() && client.IsLoggedIn() {
			return nil
		}
	}
}
