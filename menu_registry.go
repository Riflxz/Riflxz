package main

import (
	"fmt"
	"strings"
)

// menu_registry.go — Registry command terpusat.
//
// Satu-satunya tempat mendaftarkan command + deskripsi. Menu (`!menu`),
// panduan lengkap (`!allmenu`), dan menu per kategori (`!menucat`) semuanya
// di-generate dari registry ini — tidak ada lagi hardcode ganda.
//
// Cara menambah command baru:
//
//	1. daftarkan di cmdRegistry di bawah
//	2. tambahkan case-nya di commands.go
//
// (Registry ini cuma untuk tampilan menu — routing tetap di commands.go.)

// CmdInfo — metadata satu command untuk keperluan menu.
type CmdInfo struct {
	Name    string   // command utama (tanpa prefix)
	Aliases []string // alias yang juga dikenali router
	Cat     string   // kategori (lihat categoryOrder)
	Desc    string   // deskripsi singkat buat menu
	Owner   bool     // khusus owner/creator (disembunyikan dari non-owner)
	Premium bool     // owner + premium (tampil di menu premium, selain owner)
	Creator bool     // khusus creator (addowner/delowner/shell)
}

// categoryOrder — urutan tampil kategori di menu (baru = tambah di sini).
var categoryOrder = []string{
	"umum",
	"search",
	"downloader",
	"tools",
	"image",
	"konversi",
	"sticker",
	"ai",
	"fun",
	"audio",
	"video",
	"alight motion",
	"sender",
	"saluran",
	"grup",
	"jpm",
	"owner",
}

// categoryEmoji — emoji per kategori.
var categoryEmoji = map[string]string{
	"umum":          "📋",
	"search":        "🔎",
	"downloader":    "⬇️",
	"tools":         "🔧",
	"image":         "🎨",
	"konversi":      "🔄",
	"sticker":       "🖼️",
	"ai":            "🤖",
	"fun":           "🎉",
	"audio":         "📞",
	"video":         "🎬",
	"alight motion": "🎞️",
	"sender":        "🤖",
	"saluran":       "📡",
	"grup":          "🛡️",
	"jpm":           "📣",
	"owner":         "👑",
}

// cmdRegistry — daftar semua command. Wajib sinkron dengan switch di commands.go.
var cmdRegistry = []CmdInfo{
	// ── Umum ────────────────────────────────────────────────────────────────
	{Name: "menu", Aliases: []string{"help", "h"}, Cat: "umum", Desc: "Menu ringkas"},
	{Name: "allmenu", Aliases: []string{"all"}, Cat: "umum", Desc: "Panduan lengkap semua command"},
	{Name: "ping", Aliases: []string{"p"}, Cat: "umum", Desc: "Cek latency & status bot"},
	{Name: "info", Cat: "umum", Desc: "Info pesan saat ini"},
	{Name: "myjid", Aliases: []string{"jid"}, Cat: "umum", Desc: "Lihat JID kamu"},
	{Name: "owner", Aliases: []string{"cekowner", "whoami"}, Cat: "umum", Desc: "Cek status akses kamu"},
	{Name: "sticker", Aliases: []string{"s", "stk"}, Cat: "umum", Desc: "Reply media → sticker"},
	{Name: "menucat", Aliases: []string{"mc", "category"}, Cat: "umum", Desc: "Lihat command per kategori"},
	{Name: "donasi", Aliases: []string{"donate", "donation", "qris"}, Cat: "umum", Desc: "QRIS donasi bot"},
	{Name: "contributor", Aliases: []string{"tqto", "thanks"}, Cat: "umum", Desc: "Lihat kontributor bot"},
	{Name: "afk", Cat: "umum", Desc: "Tandai sedang AFK (mention kamu akan diberi tahu)"},

	// ── Search ──────────────────────────────────────────────────────────────
	{Name: "yts", Aliases: []string{"youtube", "youtubesearch"}, Cat: "search", Desc: "Cari video YouTube"},
	{Name: "wiki", Aliases: []string{"wikipedia", "wikiid"}, Cat: "search", Desc: "Cari artikel Wikipedia"},
	{Name: "kbbi", Cat: "search", Desc: "Definisi kata KBBI"},
	{Name: "lyrics", Aliases: []string{"lirik"}, Cat: "search", Desc: "Cari lirik lagu"},
	{Name: "pinterest", Aliases: []string{"pin"}, Cat: "search", Desc: "Cari gambar Pinterest"},
	{Name: "bingimage", Aliases: []string{"bingimg"}, Cat: "search", Desc: "Cari gambar Bing"},
	{Name: "applemusic", Aliases: []string{"amusic"}, Cat: "search", Desc: "Cari lagu Apple Music"},

	// ── Downloader ──────────────────────────────────────────────────────────
	{Name: "tt", Aliases: []string{"tiktok", "ttdl"}, Cat: "downloader", Desc: "Download video/foto TikTok"},
	{Name: "tthd", Aliases: []string{"tiktokhd"}, Cat: "downloader", Desc: "Download video TikTok versi HD (hd=1)"},
	{Name: "ytmp3", Aliases: []string{"yta"}, Cat: "downloader", Desc: "YouTube → MP3"},
	{Name: "ytplay", Aliases: []string{"ytp"}, Cat: "downloader", Desc: "YouTube → video + audio"},
	{Name: "playch", Cat: "downloader", Desc: "Putar lagu ke saluran (pilih channel)", Owner: true},
	{Name: "ig", Aliases: []string{"igdl", "instagram"}, Cat: "downloader", Desc: "Download foto/video Instagram"},
	{Name: "fb", Aliases: []string{"fbdl", "facebook"}, Cat: "downloader", Desc: "Download video Facebook"},
	{Name: "soundcloud", Aliases: []string{"scmusic"}, Cat: "downloader", Desc: "Download audio SoundCloud"},
	{Name: "douyin", Cat: "downloader", Desc: "Download video Douyin"},

	// ── Tools ───────────────────────────────────────────────────────────────
	{Name: "tr", Aliases: []string{"translate", "tran"}, Cat: "tools", Desc: "Terjemahkan teks"},
	{Name: "qr", Aliases: []string{"qrcode"}, Cat: "tools", Desc: "Buat QR code"},
	{Name: "ss", Aliases: []string{"ssweb", "webss"}, Cat: "tools", Desc: "Screenshot website"},
	{Name: "barcode", Cat: "tools", Desc: "Buat barcode"},
	{Name: "tempmail", Aliases: []string{"tmail", "cekmail"}, Cat: "tools", Desc: "Email sementara + cek inbox"},
	{Name: "whatmusic", Aliases: []string{"wmusic", "whatmusik"}, Cat: "tools", Desc: "Identifikasi lagu dari audio"},
	{Name: "idch", Aliases: []string{"cekidch"}, Cat: "tools", Desc: "Cek ID channel WA"},
	{Name: "idgc", Aliases: []string{"cekidgc"}, Cat: "tools", Desc: "Cek ID grup WA"},
	{Name: "kodebahasa", Aliases: []string{"kodebhs"}, Cat: "tools", Desc: "Daftar kode bahasa untuk !tr"},
	{Name: "ipinfo", Aliases: []string{"ip", "iplookup", "whois"}, Cat: "tools", Desc: "Lookup info IP"},
	{Name: "fetch", Aliases: []string{"geturl"}, Cat: "tools", Desc: "Ambil isi URL (JSON/teks)", Owner: true},
	{Name: "source", Aliases: []string{"websource"}, Cat: "tools", Desc: "Lihat source HTML website", Owner: true},
	{Name: "bypass", Aliases: []string{"bypasslink"}, Cat: "tools", Desc: "Bypass URL shortener", Owner: true},
	{Name: "bl", Aliases: []string{"blacklist"}, Cat: "owner", Desc: "Blacklist user (add/del/list)", Owner: true},
	{Name: "clear", Aliases: []string{"clearcache"}, Cat: "owner", Desc: "Bersihkan cache file temp", Owner: true},
	{Name: "enc", Aliases: []string{"encode"}, Cat: "tools", Desc: "Encode Base64", Owner: true},
	{Name: "uploadgh", Aliases: []string{"ghupload", "tourlgh"}, Cat: "tools", Desc: "Upload media ke GitHub", Owner: true},
	{Name: "rch", Aliases: []string{"reactch"}, Cat: "tools", Desc: "Reaksi ke post channel WA", Owner: true},

	// ── Image Tools ─────────────────────────────────────────────────────────
	{Name: "hd", Aliases: []string{"hdr"}, Cat: "image", Desc: "Perjelas gambar"},
	// {Name: "hdvid", Aliases: []string{"hdvideo", "vhd", "enhancevid", "hdv"}, Cat: "tools", Desc: "Perjelas video — kecil di-upscale, besar di-sharpen (reply video)"}, // ⚠️ DISABLED sementara — ffmpeg berat
	{Name: "removebg", Aliases: []string{"rbg"}, Cat: "image", Desc: "Hapus background gambar"},
	{Name: "blur", Cat: "image", Desc: "Blur gambar"},
	{Name: "cap", Aliases: []string{"caption"}, Cat: "image", Desc: "Tambah caption gelap"},
	{Name: "wanted", Cat: "image", Desc: "Buat poster wanted"},
	{Name: "compress", Aliases: []string{"kompres"}, Cat: "image", Desc: "Kompres media"},

	// ── Konversi ────────────────────────────────────────────────────────────
	{Name: "tomp3", Aliases: []string{"mp3", "toaudio"}, Cat: "konversi", Desc: "Video/audio → MP3"},
	{Name: "toimg", Aliases: []string{"stk2img"}, Cat: "konversi", Desc: "Sticker → PNG"},
	{Name: "togif", Aliases: []string{"tomp4", "stk2mp4"}, Cat: "konversi", Desc: "Sticker animasi → MP4"},
	{Name: "tourl", Aliases: []string{"tolink", "upload"}, Cat: "konversi", Desc: "Media → link URL"},
	{Name: "tofile", Cat: "konversi", Desc: "Teks → file download"},
	{Name: "resend", Aliases: []string{"asdokumen", "dokumen"}, Cat: "konversi", Desc: "Kirim ulang media tanpa kompres (reply foto/video)"},
	{Name: "rvo", Aliases: []string{"readviewonce"}, Cat: "konversi", Desc: "Baca pesan view-once"},
	{Name: "delmsg", Cat: "konversi", Desc: "Hapus pesan (owner)", Owner: true},

	// ── Sticker & Maker ─────────────────────────────────────────────────────
	{Name: "tenor", Cat: "sticker", Desc: "Cari sticker by kata kunci"},
	{Name: "sai", Aliases: []string{"stickerai"}, Cat: "sticker", Desc: "Jadikan sticker jadi sticker AI (reply sticker)"},
	{Name: "brat", Cat: "sticker", Desc: "Buat sticker brat dari teks"},
	{Name: "bratvid", Cat: "sticker", Desc: "Buat sticker animasi brat"},
	{Name: "smeme", Cat: "sticker", Desc: "Reply gambar → meme sticker"},
	{Name: "iqc", Aliases: []string{"fakeiphonechat"}, Cat: "sticker", Desc: "Buat fake chat iPhone"},
	{Name: "qc", Aliases: []string{"fc", "fakechat", "quotecard"}, Cat: "sticker", Desc: "Buat quote card"},
	{Name: "codesnap", Cat: "sticker", Desc: "Kode → screenshot"},
	{Name: "tohitam", Aliases: []string{"toputih", "tozombie", "toroblox", "tomirror", "tochibi", "toghibli", "tojapanese", "tojepang", "tolego", "toreal", "totua", "tomoai", "tomonyet", "topacar", "toroh", "totato", "toviking", "tobotak", "tofunk", "tofigura", "tohijab", "tokacamata", "tokamboja", "toliquor", "tomaid", "topeci", "topiramida", "tounderground"}, Cat: "sticker", Desc: "29 efek AI gambar (reply foto)"},

	// ── Fun (Batch 1) ───────────────────────────────────────────────────────
	{Name: "gempa", Aliases: []string{"bmkg", "infogempa", "earthquake"}, Cat: "fun", Desc: "Info gempa terkini BMKG"},
	{Name: "rate", Aliases: []string{"nilai", "rating"}, Cat: "fun", Desc: "Minta bot memberi rating"},
	{Name: "cekkhodam", Aliases: []string{"khodam", "cekhodam"}, Cat: "fun", Desc: "Cek khodam kamu"},
	{Name: "quran", Aliases: []string{"surah", "alquran", "bacaquran"}, Cat: "fun", Desc: "Baca ayat Al-Quran"},
	{Name: "githubstalk", Aliases: []string{"ghstalk", "stalkgh"}, Cat: "fun", Desc: "Stalk akun GitHub"},
	{Name: "scanrepo", Aliases: []string{"scan"}, Cat: "fun", Desc: "Scan repo GitHub untuk security risk"},

	// ── Fun (Batch 2) ───────────────────────────────────────────────────────
	{Name: "jadwalsholat", Aliases: []string{"sholat", "prayertime", "jadwalsalat"}, Cat: "fun", Desc: "Jadwal sholat hari ini"},
	{Name: "news", Aliases: []string{"beritanews"}, Cat: "fun", Desc: "Berita terkini (antara/cnn/cnbc/sindonews)"},
	{Name: "npmstalk", Aliases: []string{"stalknpm"}, Cat: "fun", Desc: "Stalk username npm"},
	{Name: "ceknik", Aliases: []string{"nik", "nikparser"}, Cat: "fun", Desc: "Parse & validasi NIK KTP"},
	{Name: "emojitoimage", Aliases: []string{"emojipng", "emojimg"}, Cat: "fun", Desc: "Emoji → gambar HD"},
	{Name: "tiktokstalk", Aliases: []string{"stalktiktok", "ttstalk"}, Cat: "fun", Desc: "Stalk akun TikTok"},
	{Name: "howgay", Aliases: []string{"gaycheck"}, Cat: "fun", Desc: "Cek tingkat kegayuan"},
	{Name: "mediafire", Aliases: []string{"mfdl", "mf"}, Cat: "downloader", Desc: "Download file MediaFire"},

	// ── Fun (Batch 3) ───────────────────────────────────────────────────────
	{Name: "getpaste", Aliases: []string{"pastebin", "getpb"}, Cat: "tools", Desc: "Ambil konten Pastebin"},
	{Name: "cekganteng", Aliases: []string{"ganteng", "handsome"}, Cat: "fun", Desc: "Cek tingkat kegantengan"},
	{Name: "cekbucin", Aliases: []string{"bucin"}, Cat: "fun", Desc: "Cek tingkat kebucinan"},
	{Name: "robloxstalk", Aliases: []string{"rblxstalk", "rbxstalk", "stalkroblox"}, Cat: "fun", Desc: "Stalk akun Roblox"},
	{Name: "jodoh", Aliases: []string{"match", "ship"}, Cat: "fun", Desc: "Cek kecocokan dua nama"},
	{Name: "editimg", Aliases: []string{"imgedit", "nanoedit"}, Cat: "ai", Desc: "Edit gambar pakai AI (reply foto)"},
	{Name: "ai", Aliases: []string{"yuuki", "yuki"}, Cat: "ai", Desc: "AI chat Yuuki — on/off, list, load session", Owner: true},

	// ── Fun (Batch 4) ───────────────────────────────────────────────────────
	{Name: "murrotal", Aliases: []string{"murottal", "audioquran", "quraudio"}, Cat: "fun", Desc: "Dengar audio murottal Al-Quran"},
	{Name: "meme", Aliases: []string{"randommeme"}, Cat: "fun", Desc: "Random meme Reddit"},
	{Name: "animeinfo", Aliases: []string{"animesearch", "anime"}, Cat: "search", Desc: "Cari info anime (MyAnimeList)"},

	// ── Fun (Batch 5) ───────────────────────────────────────────────────────
	{Name: "jadwalbola", Aliases: []string{"bola", "football", "jadwalsepakbola"}, Cat: "fun", Desc: "Jadwal bola hari ini (TheSportsDB)"},
	{Name: "truth", Aliases: []string{"truthq"}, Cat: "fun", Desc: "Random pertanyaan truth"},
	{Name: "dare", Cat: "fun", Desc: "Random tantangan dare"},
	{Name: "dns", Cat: "tools", Desc: "DNS lookup (A/AAAA/MX/NS/TXT)"},

	// ── Fun (Batch 6) ───────────────────────────────────────────────────────
	{Name: "wastalk", Cat: "tools", Desc: "Cek nomor terdaftar di WhatsApp"},
	{Name: "lookup", Cat: "tools", Desc: "DNS + WHOIS lookup domain"},
	{Name: "dafont", Aliases: []string{"font", "daffont", "carifont"}, Cat: "tools", Desc: "Cari & download font DaFont"},

	// ── Chat Formats (Baileys modern → whatsmeow) ──────────────────────────
	{Name: "poll", Aliases: []string{"polling", "jajakpendapat"}, Cat: "tools", Desc: "Buat polling interaktif"},
	{Name: "kontak", Aliases: []string{"contact", "vcard"}, Cat: "tools", Desc: "Kirim kartu kontak (vCard)"},
	{Name: "lokasi", Aliases: []string{"location", "maps"}, Cat: "tools", Desc: "Kirim titik lokasi"},
	{Name: "vo", Aliases: []string{"viewonce"}, Cat: "tools", Desc: "Kirim ulang media sebagai view-once"},
	{Name: "ptv", Cat: "tools", Desc: "Kirim video sebagai PTV (reply video)"},
	{Name: "edit", Cat: "tools", Desc: "Edit pesan bot (reply pesan)"},
	{Name: "livlok", Aliases: []string{"livlocation", "sharelok"}, Cat: "tools", Desc: "Share live location"},
	{Name: "template", Aliases: []string{"tbutton", "buttons"}, Cat: "tools", Desc: "Pesan dengan tombol (quick reply/URL)"},
	{Name: "tabel", Aliases: []string{"table", "sendtable"}, Cat: "tools", Desc: "Render tabel box-drawing"},
	{Name: "doc", Aliases: []string{"document"}, Cat: "tools", Desc: "Kirim media sebagai dokumen"},
	{Name: "semat", Aliases: []string{"pinmsg", "pinchat"}, Cat: "tools", Desc: "Sematkan pesan di grup"},

	// ── Audio Call (owner) ──────────────────────────────────────────────────
	{Name: "play", Aliases: []string{"playcall", "pc"}, Cat: "audio", Desc: "Putar lagu ke target", Owner: true, Premium: true},
	{Name: "skip", Aliases: []string{"sk"}, Cat: "audio", Desc: "Skip lagu sekarang", Owner: true},
	{Name: "stop", Aliases: []string{"stopcall", "sc"}, Cat: "audio", Desc: "Hentikan call", Owner: true},
	{Name: "lanjut", Aliases: []string{"continue", "cnt"}, Cat: "audio", Desc: "Tambah lagu ke antrian", Owner: true},
	{Name: "queue", Aliases: []string{"antrian", "q"}, Cat: "audio", Desc: "Antrian lagu (publik)"},
	{Name: "antri", Aliases: []string{"cekantri"}, Cat: "audio", Desc: "Antrian lagu (owner)", Owner: true},
	{Name: "hapus", Aliases: []string{"del"}, Cat: "audio", Desc: "Hapus lagu dari antrian", Owner: true},
	{Name: "prank", Cat: "audio", Desc: "Sisipin audio ke call", Owner: true},

	// ── Video Call (owner) ──────────────────────────────────────────────────
	{Name: "video", Aliases: []string{"playvideo", "pv"}, Cat: "video", Desc: "Video call YouTube", Owner: true},
	{Name: "skipvideo", Aliases: []string{"skv"}, Cat: "video", Desc: "Skip video sekarang", Owner: true},
	{Name: "stopvideo", Aliases: []string{"sv"}, Cat: "video", Desc: "Hentikan video call", Owner: true},
	{Name: "antrianvideo", Aliases: []string{"qv"}, Cat: "video", Desc: "Antrian video (publik)"},

	// ── Sender (owner) ──────────────────────────────────────────────────────
	{Name: "addsender", Aliases: []string{"adds"}, Cat: "sender", Desc: "Tambah akun penelpon", Owner: true},
	{Name: "canceladd", Aliases: []string{"cancels"}, Cat: "sender", Desc: "Batalkan pairing", Owner: true},
	{Name: "listsender", Aliases: []string{"senders", "ls"}, Cat: "sender", Desc: "Status semua sender", Owner: true},

	// ── Saluran WA (owner) ──────────────────────────────────────────────────
	{Name: "getidch", Cat: "saluran", Desc: "Dapatkan ID saluran dari link channel"},
	{Name: "upch", Aliases: []string{"sendch"}, Cat: "saluran", Desc: "Post ke status saluran", Owner: true},
	// {Name: "upswch", Aliases: []string{"setstatusch"}, Cat: "saluran", Desc: "Set status saluran (header channel)", Owner: true}, // ⚠️ DISABLED sementara
	{Name: "kirim", Aliases: []string{"kirimch"}, Cat: "saluran", Desc: "Kirim ke saluran lain", Owner: true},
	{Name: "saluran", Aliases: []string{"listsaluran"}, Cat: "saluran", Desc: "Daftar saluran yang diikuti", Owner: true},
	{Name: "fetchch", Aliases: []string{"fetchchannel"}, Cat: "saluran", Desc: "Fetch pesan channel (debug protobuf)", Owner: true},
	{Name: "gstatus", Aliases: []string{"swgc2"}, Cat: "saluran", Desc: "Post ke tab Updates grup", Owner: true},

	// ── Grup (jaga grup) ─────────────────────────────────────────────────────
	{Name: "jaga", Aliases: []string{"guard"}, Cat: "grup", Desc: "Status jaga grup (antilink/antitoxic/welcome)"},
	{Name: "antilink", Cat: "grup", Desc: "Peringatkan pesan ber-link (admin grup)"},
	{Name: "antitoxic", Cat: "grup", Desc: "Peringatkan kata kasar (admin grup)"},
	{Name: "welcome", Cat: "grup", Desc: "Sambut member baru (admin grup)"},
	{Name: "setwelcome", Cat: "grup", Desc: "Pesan welcome custom (@user/@group)"},
	{Name: "close", Aliases: []string{"kunci"}, Cat: "grup", Desc: "Kunci grup (hanya admin bisa chat)"},
	{Name: "open", Aliases: []string{"buka"}, Cat: "grup", Desc: "Buka grup untuk semua member"},
	{Name: "kick", Aliases: []string{"tendang"}, Cat: "grup", Desc: "Keluarkan member (reply/nomor, bisa multi)"},
	{Name: "add", Aliases: []string{"tambahangota"}, Cat: "grup", Desc: "Tambah member via nomor (bisa multi)"},
	{Name: "promote", Aliases: []string{"naikadmin"}, Cat: "grup", Desc: "Angkat member jadi admin"},
	{Name: "demote", Aliases: []string{"turunadmin"}, Cat: "grup", Desc: "Turunkan admin jadi member"},
	{Name: "tagall", Aliases: []string{"tag", "tall"}, Cat: "grup", Desc: "Tag semua member"},
	{Name: "hidetag", Aliases: []string{"htag"}, Cat: "grup", Desc: "Pesan + tag semua member"},
	{Name: "setname", Aliases: []string{"setsubject"}, Cat: "grup", Desc: "Ganti nama grup"},
	{Name: "setdesc", Aliases: []string{"setdesk"}, Cat: "grup", Desc: "Ganti deskripsi grup"},
	{Name: "setppgc", Aliases: []string{"setpp"}, Cat: "grup", Desc: "Ganti foto grup (reply gambar)"},
	{Name: "linkgc", Aliases: []string{"linkgrup"}, Cat: "grup", Desc: "Ambil link undangan grup"},
	{Name: "revoke", Aliases: []string{"resetlink"}, Cat: "grup", Desc: "Reset link undangan grup"},
	{Name: "infogc", Aliases: []string{"groupinfo", "infogrup"}, Cat: "grup", Desc: "Info grup (nama/id/owner/member)"},
	{Name: "warn", Cat: "grup", Desc: "Warn member (3× = kick otomatis)"},
	{Name: "warnlist", Aliases: []string{"listwarn"}, Cat: "grup", Desc: "Daftar warn semua member"},
	{Name: "resetwarn", Aliases: []string{"hapuswarn"}, Cat: "grup", Desc: "Reset warn member/grup"},
	{Name: "out", Aliases: []string{"keluar"}, Cat: "grup", Desc: "Bot keluar dari grup"},

	// ── JPM Broadcast (owner) ────────────────────────────────────────────────
	{Name: "jpm", Aliases: []string{"jasher", "jaser"}, Cat: "jpm", Desc: "Broadcast pesan ke semua grup (menu interaktif)", Owner: true},
	{Name: "jpmht", Aliases: []string{"jpmhidetag"}, Cat: "jpm", Desc: "Broadcast + tag semua member", Owner: true},
	{Name: "jpmch", Aliases: []string{"jpmchannel"}, Cat: "jpm", Desc: "Broadcast ke semua saluran", Owner: true},
	{Name: "autojpm", Aliases: []string{"autojasher"}, Cat: "jpm", Desc: "Auto-broadcast terjadwal (interval min 15 menit)", Owner: true},
	{Name: "stopjpm", Aliases: []string{"stopjasher"}, Cat: "jpm", Desc: "Hentikan JPM yang berjalan", Owner: true},
	{Name: "setdelayjpm", Aliases: []string{"delayjpm", "jedajpm", "setjedajpm"}, Cat: "jpm", Desc: "Atur jeda antar grup (1-30 detik)", Owner: true},
	{Name: "bljpm", Aliases: []string{"jpmbl", "jpmblacklist", "blacklistjpm"}, Cat: "jpm", Desc: "Blacklist grup dari JPM", Owner: true},
	{Name: "blautojpm", Aliases: []string{"autojpmbl", "blacklistautojpm"}, Cat: "jpm", Desc: "Blacklist grup dari auto-JPM", Owner: true},
	{Name: "jpmupdate", Aliases: []string{"updatejpm", "broadcastupdate"}, Cat: "jpm", Desc: "Broadcast info update bot", Owner: true},

	// ── Alight Motion ────────────────────────────────────────────────────────
	{Name: "am-send", Aliases: []string{"amsend", "sendlink"}, Cat: "alight motion", Desc: "Kirim magic link aktivasi Alight Motion"},
	{Name: "am-aktif", Aliases: []string{"amaktif", "aktifasi"}, Cat: "alight motion", Desc: "Aktivasi Alight Motion pakai magic link"},
	{Name: "amkey", Aliases: []string{"am-key"}, Cat: "alight motion", Desc: "Set API key Alight Motion (10k/bln)"},
	{Name: "bulk", Aliases: []string{"bulkalight"}, Cat: "alight motion", Desc: "Buat akun Alight Motion premium (max 1)"},
	{Name: "cancel", Aliases: nil, Cat: "alight motion", Desc: "Batalkan proses Alight Motion yang berjalan"},

	// ── Owner/Creator ───────────────────────────────────────────────────────
	{Name: "addowner", Aliases: []string{"ao"}, Cat: "owner", Desc: "Tambah owner (creator)", Owner: true, Creator: true},
	{Name: "delowner", Aliases: []string{"do"}, Cat: "owner", Desc: "Hapus owner (creator)", Owner: true, Creator: true},
	{Name: "addprem", Aliases: []string{"ap"}, Cat: "owner", Desc: "Tambah premium", Owner: true},
	{Name: "delprem", Aliases: []string{"dp"}, Cat: "owner", Desc: "Hapus premium", Owner: true},
	{Name: "shell", Aliases: []string{"exec", "sh"}, Cat: "owner", Desc: "Jalankan perintah shell (creator)", Owner: true, Creator: true},
	{Name: "self", Cat: "owner", Desc: "Bot hanya respon owner", Owner: true},
	{Name: "public", Cat: "owner", Desc: "Buka akses umum", Owner: true},
	{Name: "kacang", Aliases: []string{"onlygrup"}, Cat: "owner", Desc: "Mode onlygrup: PM hanya owner (on/off)", Owner: true},
	{Name: "setmenu", Cat: "owner", Desc: "Ganti versi menu", Owner: true},
	{Name: "read", Aliases: []string{"autoread"}, Cat: "owner", Desc: "Auto-read pesan masuk (on/off)", Owner: true},
}

// ─── Helper ──────────────────────────────────────────────────────────────────

// catTitle — judul kategori dengan emoji.
func catTitle(cat string) string {
	return categoryEmoji[cat] + " " + strings.ToUpper(cat)
}

// cmdsByCat — group command per kategori, sudah difilter peran & diurutkan.
// owner:false → hapus command Owner (kecuali Premium:true yang tetap tampil
// buat premium). creator:false → hapus command Creator (addowner/delowner/
// shell tetap tampil buat creator tapi tidak buat owner biasa).
func cmdsByCat(owner, creator, premium bool) map[string][]CmdInfo {
	out := make(map[string][]CmdInfo)
	for _, c := range cmdRegistry {
		if c.Owner && !owner && !(c.Premium && premium) {
			continue
		}
		if c.Creator && !creator {
			continue
		}
		out[c.Cat] = append(out[c.Cat], c)
	}
	return out
}

// visibleCats — kategori yang punya command (sesuai peran), urut dari
// categoryOrder; kategori tanpa entry order ditaruh di akhir.
func visibleCats(owner, creator, premium bool) []string {
	byCat := cmdsByCat(owner, creator, premium)
	var cats []string
	seen := map[string]bool{}
	for _, cat := range categoryOrder {
		if len(byCat[cat]) > 0 {
			cats = append(cats, cat)
			seen[cat] = true
		}
	}
	for cat := range byCat {
		if !seen[cat] {
			cats = append(cats, cat)
		}
	}
	return cats
}

// catCount — jumlah command dalam satu kategori (peran difilter).
func catCount(cat string, owner, creator, premium bool) int {
	return len(cmdsByCat(owner, creator, premium)[cat])
}

// catByKey — cari kategori dari kata kunci user (nama kategori case-insensitive
// atau emoji-nya). Return "" kalau tidak ketemu.
func catByKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, cat := range categoryOrder {
		if key == cat || key == categoryEmoji[cat] || strings.HasPrefix(cat, key) && len(key) >= 3 {
			return cat
		}
	}
	return ""
}

// fmtCmdLine — satu baris command: `!name` — desc (pakai alias terpendek kalau ada).
func fmtCmdLine(c CmdInfo, p string) string {
	return fmt.Sprintf("`%s%s` — %s", p, c.Name, c.Desc)
}
