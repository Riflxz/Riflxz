package main

import (
	"strings"
	"testing"
)

// Fixture mini dari struktur asli islamipedia.id/murottal/.
const surahFixture = `<div class="surah-item" data-audio="https://cdn.equran.id/audio-full/Misyari-Rasyid-Al-Afasi/001.mp3" data-title="Al-Fatihah"><div><h5 style="font-weight: 600;">1. Al-Fatihah</h5><p>Pembukaan</p></div></div><div class="surah-item" data-audio="https://cdn.equran.id/audio-full/Misyari-Rasyid-Al-Afasi/002.mp3" data-title="Al-Baqarah"><div><h5 style="font-weight: 600;">2. Al-Baqarah</h5><p>Sapi Betina</p></div></div><div class="surah-item" data-audio="https://cdn.equran.id/audio-full/Misyari-Rasyid-Al-Afasi/003.mp3" data-title="Ali &#039;Imran"><div><h5 style="font-weight: 600;">3. Ali &#039;Imran</h5><p>Keluarga Imran</p></div></div>`

func TestParseSurahList(t *testing.T) {
	list := parseSurahList(surahFixture)
	if len(list) != 3 {
		t.Fatalf("harusnya 3 surah, dapat %d", len(list))
	}
	if list[0].No != 1 || list[0].Nama != "Al-Fatihah" || list[0].Arti != "Pembukaan" {
		t.Errorf("surah 1 salah: %+v", list[0])
	}
	if !strings.Contains(list[0].Audio, "001.mp3") {
		t.Errorf("audio surah 1 salah: %s", list[0].Audio)
	}
	// HTML entity &#039; harus jadi apostrof.
	if list[2].Nama != "Ali 'Imran" {
		t.Errorf("unescape entity gagal: %q", list[2].Nama)
	}
}

func TestFindSurah(t *testing.T) {
	list := parseSurahList(surahFixture)

	// Cari by nama (case & spasi-insensitive, seperti plugin asli).
	if s := findSurah(list, "AL-FATIHAH"); s == nil || s.No != 1 {
		t.Errorf("cari 'AL-FATIHAH' gagal: %+v", s)
	}
	if s := findSurah(list, "al fatihah"); s == nil || s.No != 1 {
		t.Errorf("cari 'al fatihah' gagal: %+v", s)
	}
	// Cari by nomor.
	if s := findSurah(list, "2"); s == nil || s.Nama != "Al-Baqarah" {
		t.Errorf("cari '2' gagal: %+v", s)
	}
	// Cari surah ber-entity.
	if s := findSurah(list, "ali imran"); s == nil || s.No != 3 {
		t.Errorf("cari 'ali imran' gagal: %+v", s)
	}
	// Tidak ditemukan.
	if s := findSurah(list, "zzz"); s != nil {
		t.Errorf("seharusnya tidak ketemu: %+v", s)
	}
	// Query kosong tidak boleh match sembarangan.
	if s := findSurah(list, ""); s != nil {
		t.Errorf("query kosong tidak boleh match: %+v", s)
	}
}
