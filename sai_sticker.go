package main

// sai_sticker.go — command !sai (Sticker AI).
//
// Port dari Anya MD-Update toai.js + Ourin sticker.js:
//   - Reply sticker biasa → dikirim ulang dengan flag protobuf isAiSticker
//     (WhatsApp menampilkan label "AI" pada sticker itu).
//   - Gambar/video → dikonversi jadi sticker (opsi --crop/--resize/--circle/
//     --rounded, packname/author default "Yuuki AI") lalu dikirim dengan
//     flag isAiSticker yang sama.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// saiMaxVideoDuration — batas durasi video untuk sticker (detik), sama
// seperti plugin JS aslinya.
const saiMaxVideoDuration = 10.0

// ─── Opsi ────────────────────────────────────────────────────────────────────

type saiOptions struct {
	crop    bool
	circle  bool
	rounded bool
	resizeW int // 0 = tanpa resize
	resizeH int
	pack    string // packname (default "Yuuki AI")
	author  string // author (default BotDeveloper)
}

// parseSaiOptions — parse argumen seperti plugin JS:
//
//	--crop / -c        → crop jadi kotak
//	--resize / -r WxH  → resize ke ukuran tertentu
//	--circle           → bentuk lingkaran
//	--rounded          → sudut melengkung
//	<Pack> <Author>    → dua argumen non-flag pertama = packname & author
func parseSaiOptions(args []string) saiOptions {
	var o saiOptions
	for i := 0; i < len(args); i++ {
		switch strings.ToLower(args[i]) {
		case "--crop", "-c":
			o.crop = true
		case "--resize", "-r":
			if i+1 < len(args) {
				if w, h, ok := parseResize(args[i+1]); ok {
					o.resizeW, o.resizeH = w, h
					i++
				}
			}
		case "--circle":
			o.circle = true
		case "--rounded":
			o.rounded = true
		default:
			if strings.HasPrefix(args[i], "-") {
				continue // flag tak dikenal: abaikan
			}
			if o.pack == "" {
				o.pack = args[i]
			} else if o.author == "" {
				o.author = args[i]
			}
		}
	}
	return o
}

func parseResize(s string) (w, h int, ok bool) {
	parts := strings.SplitN(strings.ToLower(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// ─── Filter ffmpeg ───────────────────────────────────────────────────────────

// saiCircleFilter — geq alpha: bulatkan area di luar lingkaran (JS pakai
// ekspresi yang sama). W/H di sini = ukuran frame input geq.
const saiCircleFilter = "format=rgba,geq=r='r(X,Y)':g='g(X,Y)':b='b(X,Y)':a='if(gt(pow(X-W/2,2)+pow(Y-H/2,2),pow(min(W,H)/2,2)),0,255)'"

// saiRoundedFilter — geq alpha: pojok radius 50 (sama seperti plugin JS).
const saiRoundedFilter = "format=rgba,geq=r='r(X,Y)':g='g(X,Y)':b='b(X,Y)':a='if(lt(X,50)*lt(Y,50)*gt(pow(50-X,2)+pow(50-Y,2),pow(50,2)),0,if(gt(X,W-50)*lt(Y,50)*gt(pow(X-W+50,2)+pow(50-Y,2),pow(50,2)),0,if(lt(X,50)*gt(Y,H-50)*gt(pow(50-X,2)+pow(Y-H+50,2),pow(50,2)),0,if(gt(X,W-50)*gt(Y,H-50)*gt(pow(X-W+50,2)+pow(Y-H+50,2),pow(50,2)),0,255))))'"

// saiFilterChain — susun chain filter. Urutan mengikuti plugin JS:
// resize → crop → (image saja) circle/rounded → normalize 512.
func saiFilterChain(o saiOptions, isVideo bool) string {
	var f []string

	if o.resizeW > 0 && o.resizeH > 0 {
		// Video pakai pad hitam (seperti JS), gambar pakai transparan.
		padColor := "0x00000000"
		if isVideo {
			padColor = "black"
		}
		f = append(f, fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s",
			o.resizeW, o.resizeH, o.resizeW, o.resizeH, padColor))
	}

	if o.crop {
		f = append(f, "crop='min(iw,ih)':'min(iw,ih)',scale=512:512")
	}

	// Circle/rounded hanya untuk gambar — processVideo di JS juga
	// mengabaikan dua opsi ini.
	if !isVideo {
		if o.circle {
			f = append(f, saiCircleFilter)
		}
		if o.rounded {
			f = append(f, saiRoundedFilter)
		}
	}

	// Normalize akhir: selalu 512x512 transparan biar valid sebagai sticker WA.
	normalize := "scale=512:512:force_original_aspect_ratio=decrease,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=0x00000000"
	if isVideo {
		normalize = "fps=15," + normalize
	}
	f = append(f, normalize)

	return strings.Join(f, ",")
}

// ─── Pakname/author → metadata EXIF WebP ────────────────────────────────────

// addWebPPackName — sisipkan metadata packname/author ke file WebP.
//
// REPLIKASI byte-for-byte output wa-sticker-formatter v4.4.4 + node-webpmux
// (pustaka de-facto untuk sticker WhatsApp — dipakai Stickerly & hampir
// semua bot sticker). Penyimpangan dari format ini terbukti bikin sticker
// BLANK di WhatsApp (bug nyata saat testing: webp ffmpeg legacy "VP8" yang
// ditambah chunk EXIF tanpa VP8X tampil blank walau ter-decode normal oleh
// Pillow). Layout referensi (diverifikasi dari paket npm resmi):
//
//	RIFF | WEBP | VP8X(10, flags hasEXIF=0x08) | VP8/VP8L/ANMF... | EXIF
//
// node-webpmux melakukan:
//
//  1. Kalau file belum punya VP8X (WebP legacy tanpa alpha), DIBUATKAN
//     chunk VP8X 10 byte (flags + 3 reserved + (width-1) + (height-1),
//     24-bit LE) sebagai chunk pertama, dengan bit hasEXIF (0x08) diset.
//
//  2. Kalau VP8X sudah ada (alpha/animasi), bit hasEXIF di-OR-kan ke
//     byte flags — flags lain (alpha/anim/iccp/xmp) dipertahankan.
//
//  3. Payload EXIF = TIFF (LE, magic 42, IFD 1 entry) berisi JSON,
//     TANPA prefix "Exif\x00\x00":
//
//     49 49 2a 00 | 08 00 00 00 | 01 00 | 41 57 07 00 | <len> 16 00 00 00
//     "II*\0"       IFD offset 8   1 entry  tag 0x5741,   count len(JSON)
//     (tag 0x5741 = WhatsApp)       type 7  value offset 22 → JSON
//     (UNDEFINED)
//
//     JSON — urutan key sama persis RawMetadata.js:
//     {"sticker-pack-id":"<32 hex>","sticker-pack-name":"<pack>",
//     "sticker-pack-publisher":"<author>","emojis":[]}
//
//  4. Chunk EXIF ditulis SETELAH semua chunk gambar (di akhir file).
func addWebPPackName(data []byte, pack, author string) []byte {
	if len(data) < 12 || !bytes.Equal(data[:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WEBP")) {
		return data // bukan WebP valid — kembalikan apa adanya
	}

	exif := buildStickerJSON(pack, author)

	// ── 1-2. Pastikan VP8X ada di depan dengan flag hasEXIF (0x08) ─────────
	var head []byte
	if len(data) >= 12 && bytes.Equal(data[12:16], []byte("VP8X")) {
		// VP8X sudah ada (alpha/animasi): OR flag hasEXIF ke byte flags.
		vp8xSize := int(binary.LittleEndian.Uint32(data[16:20]))
		vp8x := make([]byte, 8+vp8xSize)
		copy(vp8x, data[12:20+vp8xSize])
		vp8x[8] |= 0x08 // hasEXIF
		head = append(head, data[:12]...)
		head = append(head, vp8x...)
		head = append(head, data[20+vp8xSize+(vp8xSize&1):]...)
	} else {
		// WebP legacy (chunk pertama VP8/VP8L): buat VP8X baru di depan.
		flags := byte(0x08) // hasEXIF
		if webPFirstChunkHasAlpha(data[12:]) {
			flags |= 0x10 // hasAlpha
		}
		w, h := webPFirstChunkDims(data[12:])
		vp8x := make([]byte, 18)
		copy(vp8x[:4], "VP8X")
		binary.LittleEndian.PutUint32(vp8x[4:8], 10)
		vp8x[8] = flags
		vp8x[12] = byte(w - 1) // width-1, 24-bit LE
		vp8x[13] = byte((w - 1) >> 8)
		vp8x[14] = byte((w - 1) >> 16)
		vp8x[15] = byte(h - 1)
		vp8x[16] = byte((h - 1) >> 8)
		vp8x[17] = byte((h - 1) >> 16)
		head = append(head, data[:12]...)
		head = append(head, vp8x...)
		head = append(head, data[12:]...)
	}

	// ── 3-4. Chunk EXIF di akhir file (+padding 1 byte kalau ganjil) ──────
	exifChunk := make([]byte, 8+len(exif)+(len(exif)&1))
	copy(exifChunk[:4], "EXIF")
	binary.LittleEndian.PutUint32(exifChunk[4:8], uint32(len(exif))) // size = tanpa padding
	copy(exifChunk[8:], exif)

	out := make([]byte, 0, len(head)+len(exifChunk))
	out = append(out, head...)
	out = append(out, exifChunk...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

// buildStickerJSON — payload EXIF ala wa-sticker-formatter v4 (RawMetadata.js):
// TIFF 22 byte + JSON metadata (pack id acak, pack name, publisher, emojis).
func buildStickerJSON(pack, author string) []byte {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		// Randomness gagal — ID tetap dibuat (hex nol), WhatsApp tidak
		// menolak sticker hanya karena id berulang.
	}
	// JSON dibangun manual — urutan key & byte persis RawMetadata.js
	// (wa-sticker-formatter v4): sticker-pack-id, sticker-pack-name,
	// sticker-pack-publisher, emojis. json.Marshal tidak dipakai agar
	// tidak meng-escape & < > jadi \u0026 \u003c dan urutan key tidak
	// bergantung pada definisi struct.
	esc := func(s string) string {
		return strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s)
	}
	js := fmt.Sprintf(
		`{"sticker-pack-id":"%s","sticker-pack-name":"%s","sticker-pack-publisher":"%s","emojis":[]}`,
		hex.EncodeToString(id), esc(pack), esc(author))

	exif := make([]byte, 22+len(js))
	copy(exif, []byte{
		0x49, 0x49, 0x2a, 0x00, // "II*\0" — TIFF little-endian, magic 42
		0x08, 0x00, 0x00, 0x00, // IFD offset = 8
		0x01, 0x00, // 1 entry
		0x41, 0x57, // tag 0x5741 (WhatsApp sticker metadata)
		0x07, 0x00, // type 7 = UNDEFINED
		0x00, 0x00, 0x00, 0x00, // count = len(JSON), diisi di bawah
		0x16, 0x00, 0x00, 0x00, // value offset = 22 → JSON
	})
	binary.LittleEndian.PutUint32(exif[14:18], uint32(len(js)))
	copy(exif[22:], js)
	return exif
}

// webPFirstChunkHasAlpha — deteksi alpha pada WebP legacy: ALPH chunk ada,
// atau chunk pertama VP8L dengan bit alpha (0x10) di byte pertama payload.
// Dikenali dari offset chunk pertama (data mulai dari posisi chunk pertama).
func webPFirstChunkHasAlpha(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	pos := 0
	for pos+8 <= len(data) {
		chunk := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		total := 8 + size + (size & 1)
		if pos+total > len(data) {
			break
		}
		if chunk == "ALPH" {
			return true
		}
		if chunk == "VP8L" && pos == 0 && size >= 1 && data[pos+8]&0x10 != 0 {
			return true // bit 4 byte pertama payload VP8L = is_alpha
		}
		pos += total
	}
	return false
}

// webPFirstChunkDims — dimensi dari chunk gambar pertama (data mulai dari
// posisi chunk pertama). VP8: 14-bit LE di payload byte 6-9. VP8L: 14-bit
// di payload byte 1-6. Gagal parse → fallback 512 (ukuran sticker WA).
func webPFirstChunkDims(data []byte) (int, int) {
	if len(data) < 18 {
		return 512, 512
	}
	cc := string(data[:4])
	payload := data[8:]
	if cc == "VP8 " && len(payload) >= 10 && payload[0]&0x80 == 0 {
		w := int(payload[6]) | int(payload[7])<<8
		h := int(payload[8]) | int(payload[9])<<8
		if w > 0 && h > 0 {
			return w & 0x3fff, h & 0x3fff
		}
	}
	if cc == "VP8L" && len(payload) >= 6 && payload[0] == 0x2f {
		w := int(payload[1]) | int(payload[2])<<8 | int(payload[3])<<16
		h := int(payload[3])>>6 | int(payload[4])<<2 | int(payload[5])<<10
		if w > 0 && h > 0 {
			return w & 0x3fff, h & 0x3fff
		}
	}
	return 512, 512
}

// ─── Handler ──// ─── Handler ─────────────────────────────────────────────────────────────────

// saiResolveMedia — cari media (sticker/gambar/video) dari pesan. Prioritas
// sama seperti plugin JS: quoted message dulu, baru media pada pesan command.
func saiResolveMedia(evt *events.Message) (string, any) {
	if q := msgContextInfo(evt).GetQuotedMessage(); q != nil {
		if st := q.GetStickerMessage(); st != nil {
			return "sticker", st
		}
		if img := q.GetImageMessage(); img != nil {
			return "image", img
		}
		if vid := q.GetVideoMessage(); vid != nil {
			return "video", vid
		}
	}
	if st := evt.Message.GetStickerMessage(); st != nil {
		return "sticker", st
	}
	if img := evt.Message.GetImageMessage(); img != nil {
		return "image", img
	}
	if vid := evt.Message.GetVideoMessage(); vid != nil {
		return "video", vid
	}
	return "", nil
}

// saiVideoDuration — durasi video via ffprobe.
func saiVideoDuration(path string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := runCmdTimeout(cmd, 15*time.Second); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(out.String()), 64)
}

func saiHelp(chat types.JID) {
	sendText(context.Background(), chat,
		"🖼️ *sᴛɪᴄᴋᴇʀ AI* — `"+Prefix+"sai`\n\n"+
			"*Reply sticker biasa* → jadi sticker *AI* (label AI di WhatsApp):\n"+
			"`"+Prefix+"sai`\n\n"+
			"*Juga bisa dari gambar/video:*\n"+
			"Kirim/reply gambar atau video dengan caption `"+Prefix+"sai`\n\n"+
			"*ᴏᴘsɪ (gambar/video):*\n"+
			"> `--crop` — crop jadi kotak\n"+
			"> `--resize WxH` — resize ke ukuran\n"+
			"> `--circle` — bentuk lingkaran\n"+
			"> `--rounded` — sudut melengkung\n\n"+
			"*ᴄᴏɴᴛᴏʜ:*\n"+
			"> `"+Prefix+"sai --circle`\n"+
			"> `"+Prefix+"sai NamaPack Author`\n\n"+
			"*ℹ️ Video maksimal 10 detik*")
}

func handleSai(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat

	mediaType, media := saiResolveMedia(evt)
	if mediaType == "" {
		saiHelp(chat)
		return
	}

	reactMsg(ctx, evt, "⏳")
	opt := parseSaiOptions(strings.Fields(args))

	// Download media
	var rawBytes []byte
	var err error
	switch m := media.(type) {
	case *waE2E.ImageMessage:
		rawBytes, err = waClient.Download(ctx, m)
	case *waE2E.VideoMessage:
		rawBytes, err = waClient.Download(ctx, m)
	case *waE2E.StickerMessage:
		// Sticker input: kirim ulang langsung dengan label AI (port persis
		// Anya toai.js) — tidak perlu konversi ulang, itu hanya menurunkan
		// kualitas.
		rawBytes, err = waClient.Download(ctx, m)
	default:
		err = fmt.Errorf("tipe media tidak didukung")
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media: "+err.Error())
		return
	}

	// Sticker → langsung kirim ulang ber-label AI.
	if mediaType == "sticker" {
		if err := sendStickerPack(ctx, chat, rawBytes, true); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Gagal kirim sticker: "+err.Error())
			return
		}
		reactMsg(ctx, evt, "✅")
		return
	}

	if err := os.MkdirAll("temp", 0o755); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat temp dir")
		return
	}

	uid := uuid.New().String()
	ext := "png"
	if mediaType == "video" {
		ext = "mp4"
	}
	inPath := filepath.Join("temp", "sai_in_"+uid+"."+ext)
	outPath := filepath.Join("temp", "sai_out_"+uid+".webp")
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	if err := os.WriteFile(inPath, rawBytes, 0o644); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal tulis file temp: "+err.Error())
		return
	}

	// Video: cek durasi (maks 10 detik, sama seperti plugin JS).
	if mediaType == "video" {
		dur, err := saiVideoDuration(inPath)
		if err == nil && dur > saiMaxVideoDuration {
			reactMsg(ctx, evt, "☢")
			sendText(ctx, chat, fmt.Sprintf(
				"❌ Video terlalu panjang!\n\n> Durasi: %.1f detik\n> Maksimal: %.0f detik",
				dur, saiMaxVideoDuration))
			return
		}
	}

	// Bangun command ffmpeg — encoder beda untuk gambar vs video animasi.
	ffArgs := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath,
		"-vf", saiFilterChain(opt, mediaType == "video")}
	if mediaType == "video" {
		// -t 10 sebagai jaring pengaman kalau ffprobe gagal (lihat di atas).
		ffArgs = append(ffArgs, "-t", "10",
			"-c:v", "libwebp_anim", "-lossless", "0", "-q:v", "70", "-loop", "0")
	} else {
		ffArgs = append(ffArgs,
			"-c:v", "libwebp", "-lossless", "0", "-q:v", "80", "-preset", "picture")
	}
	ffArgs = append(ffArgs, outPath)

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", ffArgs...)
	cmd.Stderr = &stderr
	timeout := 60 * time.Second
	if mediaType == "video" {
		timeout = 90 * time.Second
	}
	if err := runCmdTimeout(cmd, timeout); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat sticker (ffmpeg): "+lastLines(stderr.String(), 3))
		return
	}

	webpBytes, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil sticker: "+err.Error())
		return
	}

	// Label AI: packname default "Yuuki AI" — identitas !sai vs !sticker biasa.
	pack := opt.pack
	if pack == "" {
		pack = BotName + " AI"
	}
	author := opt.author
	if author == "" {
		author = BotDeveloper
	}
	webpBytes = addWebPPackName(webpBytes, pack, author)

	if err := sendStickerPack(ctx, chat, webpBytes, true); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim sticker: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// sendStickerPack — upload + kirim sticker (duplikat ringan dari
// sendStickerBytes, tanpa konversi karena input sudah webp dari ffmpeg).
// isAI=true → pasang flag isAiSticker di protobuf: WhatsApp menampilkan
// badge/label "AI" pada sticker (port isAiSticker:true dari Anya toai.js).
func sendStickerPack(ctx context.Context, chat types.JID, data []byte, isAI bool) error {
	up, err := waClient.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload sticker: %w", err)
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(data))),
			Mimetype:      proto.String("image/webp"),
			IsAiSticker:   proto.Bool(isAI),
			ContextInfo:   mergeReplyCtx(ctx, newsletterCtxInfo(ctx)),
		},
	})
	return err
}
