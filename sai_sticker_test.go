package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ─── parseSaiOptions ─────────────────────────────────────────────────────────

func TestParseSaiOptionsAllFlags(t *testing.T) {
	o := parseSaiOptions([]string{"--crop", "--resize", "256x512", "--circle", "--rounded"})
	if !o.crop || !o.circle || !o.rounded {
		t.Errorf("flag crop/circle/rounded tidak semua ter-set: %+v", o)
	}
	if o.resizeW != 256 || o.resizeH != 512 {
		t.Errorf("resize = %dx%d, want 256x512", o.resizeW, o.resizeH)
	}
}

func TestParseSaiOptionsShortFlags(t *testing.T) {
	o := parseSaiOptions([]string{"-c", "-r", "128x128"})
	if !o.crop {
		t.Errorf("-c harus set crop")
	}
	if o.resizeW != 128 || o.resizeH != 128 {
		t.Errorf("resize = %dx%d, want 128x128", o.resizeW, o.resizeH)
	}
}

func TestParseSaiOptionsPackAuthor(t *testing.T) {
	o := parseSaiOptions([]string{"NamaPack", "AuthorKu"})
	if o.pack != "NamaPack" || o.author != "AuthorKu" {
		t.Errorf("pack/author = %q/%q, want NamaPack/AuthorKu", o.pack, o.author)
	}
}

func TestParseSaiOptionsBareResizeNoValue(t *testing.T) {
	// "--resize" di akhir tanpa nilai: jangan consume arg sesudahnya & jangan crash.
	o := parseSaiOptions([]string{"--resize"})
	if o.resizeW != 0 || o.resizeH != 0 {
		t.Errorf("resize tanpa nilai harus 0x0, dapat %dx%d", o.resizeW, o.resizeH)
	}
	o = parseSaiOptions([]string{"--resize", "abc"}) // nilai tidak valid
	if o.resizeW != 0 || o.resizeH != 0 {
		t.Errorf("resize 'abc' harus diabaikan, dapat %dx%d", o.resizeW, o.resizeH)
	}
}

func TestParseSaiOptionsUnknownFlagIgnored(t *testing.T) {
	o := parseSaiOptions([]string{"--nope", "--crop"})
	if !o.crop {
		t.Errorf("--crop harus tetap diproses walau ada flag tak dikenal")
	}
	if o.pack != "" || o.author != "" {
		t.Errorf("flag tak dikenal tidak boleh jadi pack/author: %+v", o)
	}
}

func TestParseSaiOptionsOnlyTwoBareArgs(t *testing.T) {
	o := parseSaiOptions([]string{"a", "b", "c"})
	if o.pack != "a" || o.author != "b" {
		t.Errorf("arg bare ketiga harus diabaikan, dapat pack=%q author=%q", o.pack, o.author)
	}
}

// ─── addWebPPackName ─────────────────────────────────────────────────────────

// fakeWebP — bikin blob RIFF WebP minimal: header + satu chunk "VP8 " kosong.
func fakeWebP() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.WriteString("\x00\x00\x00\x00") // size, diisi belakangan
	b.WriteString("WEBP")
	b.WriteString("VP8 ")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.WriteString("DATA")
	sz := uint32(b.Len() - 8)
	binary.LittleEndian.PutUint32(b.Bytes()[4:8], sz)
	return b.Bytes()
}

// TestAddWebPPackName — replikasi wa-sticker-formatter v4:
// VP8X dibuat (file legacy), flag hasEXIF (0x08), EXIF JSON di akhir.
func TestAddWebPPackName(t *testing.T) {
	in := fakeWebP() // legacy: "VP8 " 4 byte, tanpa VP8X
	out := addWebPPackName(in, "Yuuki AI", "RIflxz")

	if !bytes.Equal(out[:4], []byte("RIFF")) || !bytes.Equal(out[8:12], []byte("WEBP")) {
		t.Fatal("header RIFF/WEBP rusak setelah inject")
	}
	if want := uint32(len(out) - 8); binary.LittleEndian.Uint32(out[4:8]) != want {
		t.Errorf("RIFF size = %d, want %d", binary.LittleEndian.Uint32(out[4:8]), want)
	}

	// 1. VP8X harus JADI chunk pertama (dibuat dari legacy), flag hasEXIF.
	if !bytes.HasPrefix(out[12:16], []byte("VP8X")) {
		t.Fatalf("VP8X harus chunk pertama, dapat %q", out[12:16])
	}
	if got := out[20]; got&0x08 == 0 {
		t.Errorf("flag hasEXIF (0x08) tidak diset: flags=0x%02x", got)
	}
	// Dimensi dari fallback (payload fake tidak punya header VP8) = 512 → 511.
	if !bytes.Equal(out[24:27], []byte{0xff, 0x01, 0x00}) || !bytes.Equal(out[27:30], []byte{0xff, 0x01, 0x00}) {
		t.Errorf("VP8X dimensi salah: % x", out[24:30])
	}

	// 2. Chunk VP8 asli tetap utuh setelah VP8X.
	if !bytes.HasPrefix(out[30:34], []byte("VP8 ")) {
		t.Fatalf("chunk VP8 hilang: %q", out[30:34])
	}
	if !bytes.Contains(out, []byte("DATA")) {
		t.Error("payload VP8 hilang")
	}

	// 3. EXIF di akhir, payload JSON TANPA prefix "Exif\x00\x00".
	exifAt := 30 + 8 + 4 // setelah VP8 (size=4, genap)
	if !bytes.HasPrefix(out[exifAt:exifAt+4], []byte("EXIF")) {
		t.Fatalf("EXIF harus mengikuti VP8 di offset %d, dapat %q", exifAt, out[exifAt:exifAt+4])
	}
	exifLen := int(binary.LittleEndian.Uint32(out[exifAt+4 : exifAt+8]))
	payload := out[exifAt+8 : exifAt+8+exifLen]
	if !bytes.HasPrefix(payload, []byte{0x49, 0x49, 0x2a, 0x00}) {
		t.Fatalf("payload EXIF harus TIFF II*\x00: % x", payload[:8])
	}
	if bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
		t.Error("payload EXIF tidak boleh ber-prefix Exif\x00\x00 (pola referensi tidak memakainya)")
	}
	if !bytes.Contains(payload, []byte(`"sticker-pack-name":"Yuuki AI"`)) ||
		!bytes.Contains(payload, []byte(`"sticker-pack-publisher":"RIflxz"`)) ||
		!bytes.Contains(payload, []byte(`"emojis":[]`)) {
		t.Errorf("JSON metadata tidak lengkap: %s", payload)
	}
	// count di IFD (offset 14) harus = panjang JSON.
	jsonStart := 22
	if got := int(binary.LittleEndian.Uint32(payload[14:18])); got != exifLen-jsonStart {
		t.Errorf("count IFD = %d, want %d", got, exifLen-jsonStart)
	}
}

// TestAddWebPPackNameOddLength — JSON ganjil → chunk EXIF harus di-pad 1 byte.
func TestAddWebPPackNameOddLength(t *testing.T) {
	in := fakeWebP()
	out := addWebPPackName(in, "Pack Yang Panjang Sekali Untuk Bikin Data Ganjil", "Author")
	exifAt := 30
	exifLen := int(binary.LittleEndian.Uint32(out[exifAt+4 : exifAt+8]))
	if exifLen%2 == 1 {
		if got := out[exifAt+8+exifLen]; got != 0 {
			t.Errorf("padding byte setelah EXIF ganjil harus 0, dapat %d", got)
		}
	}
	if want := uint32(len(out) - 8); binary.LittleEndian.Uint32(out[4:8]) != want {
		t.Errorf("RIFF size tidak sinkron setelah inject (pad): %d vs %d", binary.LittleEndian.Uint32(out[4:8]), want)
	}
}

// fakeWebPWithVP8X — WebP extended: VP8X (10 byte, flags=0) + VP8 biasa —
// meniru output ffmpeg untuk sticker animasi & --circle (alpha/animasi).
func fakeWebPWithVP8X() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.WriteString("\x00\x00\x00\x00") // size, diisi belakangan
	b.WriteString("WEBP")
	b.WriteString("VP8X")
	binary.Write(&b, binary.LittleEndian, uint32(10))
	b.WriteString("\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	b.WriteString("VP8 ")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.WriteString("DATA")
	sz := uint32(b.Len() - 8)
	binary.LittleEndian.PutUint32(b.Bytes()[4:8], sz)
	return b.Bytes()
}

// TestAddWebPPackNameExistingVP8X — VP8X yang sudah ada dipertahankan di
// posisi pertama, bit hasEXIF di-OR ke flags, flags lain tidak diubah.
func TestAddWebPPackNameExistingVP8X(t *testing.T) {
	in := fakeWebPWithVP8X()
	out := addWebPPackName(in, "Pack", "Author")

	if !bytes.HasPrefix(out[12:16], []byte("VP8X")) {
		t.Fatalf("VP8X harus tetap chunk pertama, dapat %q", out[12:16])
	}
	if got := out[20]; got != 0x08 {
		t.Errorf("flags VP8X = 0x%02x, want 0x08 (hasEXIF)", got)
	}
	// VP8 asli tetap utuh setelah VP8X.
	if !bytes.HasPrefix(out[30:34], []byte("VP8 ")) {
		t.Errorf("chunk VP8 hilang setelah inject: %q", out[30:34])
	}
	if !bytes.Contains(out, []byte("DATA")) {
		t.Error("payload VP8 hilang")
	}
	// EXIF di akhir file (chunk terakhir; padding 1 byte diizinkan untuk
	// panjang ganjil, jadi cek lewat parse chunk, bukan raw suffix).
	pos := 12
	var last string
	for pos+8 <= len(out) {
		last = string(out[pos : pos+4])
		sz := int(binary.LittleEndian.Uint32(out[pos+4 : pos+8]))
		pos += 8 + sz + (sz & 1)
	}
	if last != "EXIF" {
		t.Errorf("EXIF harus chunk terakhir, dapat %q", last)
	}
	if want := uint32(len(out) - 8); binary.LittleEndian.Uint32(out[4:8]) != want {
		t.Errorf("RIFF size tidak sinkron: %d vs %d", binary.LittleEndian.Uint32(out[4:8]), want)
	}
}

// TestAddWebPPackNameNonWebP — input bukan RIFF dikembalikan apa adanya.
func TestAddWebPPackNameNonWebP(t *testing.T) {
	in := []byte("Bukan RIFF sama sekali, data acak")
	if out := addWebPPackName(in, "pack", "author"); !bytes.Equal(out, in) {
		t.Error("input non-RIFF harus dikembalikan apa adanya")
	}
}

// TestBuildStickerJSON — payload TIFF+JSON: urutan key persis RawMetadata.js,
// panjang JSON di IFD count, value offset 22.
func TestBuildStickerJSON(t *testing.T) {
	exif := buildStickerJSON(`Pack "AI" & <Co>`, "Author")
	if len(exif) <= 22 {
		t.Fatal("payload EXIF terlalu pendek")
	}
	js := exif[22:]
	if !bytes.HasPrefix(js, []byte(`{"sticker-pack-id":"`)) {
		t.Errorf("JSON harus mulai dengan sticker-pack-id: %s", js[:40])
	}
	if !bytes.Contains(js, []byte(`"sticker-pack-name":"Pack \"AI\" & <Co>"`)) {
		t.Errorf("JSON escaping salah: %s", js)
	}
	if !bytes.Contains(js, []byte(`"emojis":[]`)) {
		t.Errorf("emojis harus []: %s", js)
	}
	if got := int(binary.LittleEndian.Uint32(exif[14:18])); got != len(js) {
		t.Errorf("count IFD = %d, want %d", got, len(js))
	}
	// Tag WhatsApp (0x5741) + type UNDEFINED (7) + value offset 22.
	if !bytes.Equal(exif[10:14], []byte{0x41, 0x57, 0x07, 0x00}) {
		t.Errorf("tag/type IFD salah: % x", exif[10:14])
	}
	if !bytes.Equal(exif[18:22], []byte{0x16, 0x00, 0x00, 0x00}) {
		t.Errorf("value offset harus 22: % x", exif[18:22])
	}
}
