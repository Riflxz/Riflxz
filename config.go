package main

// ══════════════════════════════════════════════════════════════
//  🙏 HORMATI PEMBUAT
//  ──────────────────────────────────────────────────────────────
//  YuukiBot dikembangkan oleh developer asli (Riflxz) dengan
//  kerja keras. Kalau kamu memakai / memodifikasi bot ini, mohon:
//    • Jangan hapus nama developer / kredit pembuat di source.
//    • Jangan klaim bot ini sebagai buatanmu sendiri.
//    • Kalau mau mengembangkan, FORK repository asli, bukan
//      menyalin mentah-mentah.
//    • Jangan lupa ⭐ STAR repository asli sebagai bentuk
//      dukungan & apresiasi.
//  Terima kasih sudah menghargai karya orang lain. 🙏
// ══════════════════════════════════════════════════════════════

const (
	// OwnerNumber dipake buat command admin-only kalau nanti mau ditambah.
	// Format: kode negara + nomor, TANPA tanda "+", spasi, atau strip.
	// ⚠️ GANTI dengan nomor kamu sendiri sebelum dipakai.
	OwnerNumber = "628xxx"

	// CreatorNumber = nomor creator/developer bot. Satu-satunya yang bisa pakai
	// eval, shell, addowner. Sama kayak "ownerNumber" di config.json Base-Bot-Wa.
	// ⚠️ GANTI dengan nomor kamu sendiri sebelum dipakai.
	CreatorNumber = "628xxx"

	// BotNumber cuma buat informasi/README. whatsmeow login pake QR code,
	// jadi nomor ini gak dibaca langsung sama kode.
	// ⚠️ GANTI dengan nomor bot kamu sendiri sebelum dipakai.
	BotNumber = "628xxx"

	BotName      = "Yuuki"
	BotDeveloper = "Riflxz"
	ChannelName  = "MTCommunity" // nama saluran WA yang ditampilkan di footer menu

	// MenuImageURL = URL gambar untuk header menu (canvas).
	// Kosongkan kalau tidak mau pakai gambar.
	// Contoh: "https://example.com/banner.jpg"
	MenuImageURL = "https://imagetourl.cloud/ducct8r2.png"

	// Prefix command, misal "!" -> !menu, !play
	Prefix = "!"

	// Cooldown anti-spam buat .playcall, per pengirim.
	PlaycallCooldownSeconds = 30

	// TheresavAPIKey dipake buat command download video (theresav ytmp4, .playvideo).
	// Daftar dulu buat dapetin apikey-mu sendiri di https://api.theresav.biz.id
	// lalu isi di sini. Bisa juga di-override lewat environment THERESAV_APIKEY
	// (env selalu menang kalau keduanya keisi).
	//
	// 🙏 Bot ini TIDAK BERAFILIASI dengan API theresav — cuma pakai API-nya doang.
	TheresavAPIKey = "" // ← isi apikey kamu di sini

	// TheresavResolution: resolusi source yg didownload dari theresav (bukan resolusi
	// FINAL video call — itu tetep di-downscale ke 480p di videoencode.go buat
	// kompatibilitas decoder WA, JANGAN diubah di situ). Naikin ini ("1080") cuma
	// bikin hasil downscale-nya lebih tajam/bersih, karena source-nya lebih detail.
	TheresavResolution = "720"

	// Path file database JSON buat owner & premium.
	// Kalau mau ganti lokasi, ubah di sini.
	OwnerDBPath   = "database/owner.json"
	PremiumDBPath = "database/premium.json"
	JadibotDBPath = "database/jadibot.json"
	BlacklistPath = "database/blacklist.json"

	// SwGC2JID = JID channel/saluran default untuk !upch / !sendch.
	// Format channel  : "120363xxxxxxxxxx@newsletter"
	// Format grup     : "628xxx-xxxxxxxxxx@g.us"
	SwGC2JID = "120363427527083272@newsletter"

	// CookiesPath = path ke file cookies.txt untuk yt-dlp (YouTube download).
	// Default absolute — kalau file-nya tidak ada di situ, fallback ke ./cookies.txt
	// (lihat cookiesPath()).
	CookiesPath = "/YamzzBot-Caller/cookies.txt"

	// RemoveBGKey = API key remove.bg (gratis 50 kredit/bulan di remove.bg)
	RemoveBGKey = "" // ← daftar di remove.bg, gratis

	// GitHub upload config untuk !uploadgh
	GithubToken = "" // Personal Access Token
	GithubUser  = "" // username GitHub
	GithubRepo  = "" // nama repo (harus public atau token punya akses)

	// ReactChannelJWT dipake buat command !rch / !reactch (reaksi ke post channel WA).
	// Dapat dari API Omegatech. Bisa di-override lewat environment RCH_JWT
	// (env selalu menang kalau keduanya keisi).
	//
	// ⚠️ JWT ini punya masa berlaku (exp) — kalau sudah kedaluwarsa, ganti di sini
	// atau di env, tanpa perlu ubah kode.
	ReactChannelJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6IjY5OGE2ZGI5MjVjMzUyOTcxZTIyYTdkNSIsImlhdCI6MTc3NTg1NzUyMCwiZXhwIjoxNzc2NDYyMzIwfQ.q7D6potY6cl3n-ZY8nQbetNFqPSl79aF5IIZ_QbtABc"

	// MTZAPIKey dipake buat command premium !am-send / !am-aktif
	// (aktivasi akun via magic link — API mtzhacie.my.id).
	// Bisa di-override lewat environment MTZ_API_KEY (env selalu menang).
	MTZAPIKey = "api_xqC2Yvda_hoToon-xdi-rRLYXntpmTR89vKcbeIJ3XSp6OxO81mTsX_VKrhYQWT6MPQmYKrc3sgnFVMJQDCPo5biizsnQvA"
)

// isOwner: nomor owner utama. Override via .env (OWNER_NUMBER) tanpa mengubah
// setting default di atas.
func isOwner(number string) bool {
	if v := envOwnerNumber(); v != "" {
		return number == v
	}
	return number == OwnerNumber
}
