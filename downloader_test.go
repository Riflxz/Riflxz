package main

import (
	"encoding/binary"
	"testing"
)

// buildTestMP4 — susun MP4 palsu: ftyp + moov + mdat dengan ukuran box valid.
func buildTestMP4(mdatSize int) []byte {
	ftyp := []byte{0, 0, 0, 32, 'f', 't', 'y', 'p'}
	ftyp = append(ftyp, make([]byte, 24)...)
	moov := []byte{0, 0, 0, 24, 'm', 'o', 'o', 'v'}
	moov = append(moov, make([]byte, 16)...)
	mdat := make([]byte, 8+mdatSize)
	binary.BigEndian.PutUint32(mdat, uint32(8+mdatSize))
	copy(mdat[4:8], "mdat")
	out := append(ftyp, moov...)
	return append(out, mdat...)
}

// buildGarbage — trailing garbage khas CDN TikTok: box size=4 (tanpa type)
// diikuti box aneh 43KB. File dengan pola ini TETAP playable.
func buildGarbage() []byte {
	g := make([]byte, 4+43268)
	binary.BigEndian.PutUint32(g, 4) // box size=4, tanpa type
	binary.BigEndian.PutUint32(g[4:], 43268)
	return g
}

func TestMP4Playable(t *testing.T) {
	// File sehat: semua box lengkap.
	good := buildTestMP4(1000)
	if !mp4Playable(good) {
		t.Fatal("file mp4 sehat harus diterima")
	}

	// File sehat + trailing garbage CDN TikTok (kasus nyata dari link user).
	withGarbage := append(good, buildGarbage()...)
	if !mp4Playable(withGarbage) {
		t.Fatal("file sehat dengan trailing garbage CDN harus diterima")
	}

	// Terpotong di tengah mdat — persis kasus download putus.
	cut := good[:len(good)-500]
	if mp4Playable(cut) {
		t.Fatal("file terpotong harus ditolak")
	}

	// Terpotong tepat di header box.
	cut2 := good[:len(good)-10]
	if mp4Playable(cut2) {
		t.Fatal("file terpotong di header box harus ditolak")
	}

	// Bukan mp4 sama sekali.
	if mp4Playable([]byte("hello world ini bukan video")) {
		t.Fatal("file non-mp4 harus ditolak")
	}

	// Kosong.
	if mp4Playable(nil) {
		t.Fatal("file kosong harus ditolak")
	}
}