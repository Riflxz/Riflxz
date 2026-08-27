package main

import (
	"strconv"
	"testing"
)

// Unit test helper features_entertainment.go (logika murni, tanpa jaringan).
// Verifikasi live API (myquran, npm, noto-emoji, RSS, tikwm/shannz) dilakukan
// manual saat port — lihat catatan Batch 2.

func TestShortNum(t *testing.T) {
	cases := map[int]string{
		999:        "999",
		1500:       "1.5K",
		2500000:    "2.5M",
		1200000000: "1.2B",
	}
	for in, want := range cases {
		if got := shortNum(in); got != want {
			t.Errorf("shortNum(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestMonthNameID(t *testing.T) {
	if monthNameID(1) != "Januari" || monthNameID(12) != "Desember" {
		t.Fatal("nama bulan salah")
	}
	if monthNameID(0) != "?" || monthNameID(13) != "?" {
		t.Fatal("bulan invalid harus '?'")
	}
}

func TestNIKParsing(t *testing.T) {
	// NIK DKI Jakarta, lahir 21-09-2002, pria.
	nik := "3171092109020003"
	if got := provinsiNIK[nik[0:2]]; got != "DKI Jakarta" {
		t.Fatalf("provinsi = %s", got)
	}
	day, _ := strconv.Atoi(nik[6:8])
	if day != 21 {
		t.Fatalf("tanggal lahir = %d", day)
	}
	// Wanita: tanggal lahir +40 pada NIK (61 = 21 + 40).
	nikWanita := "3171096109020004"
	day, _ = strconv.Atoi(nikWanita[6:8])
	if day <= 40 {
		t.Fatalf("NIK wanita harus punya tanggal >40, dapat %d", day)
	}
	if day-40 != 21 {
		t.Fatalf("tanggal lahir wanita harus 61-40=21, dapat %d", day-40)
	}
}
