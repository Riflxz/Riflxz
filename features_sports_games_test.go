package main

import (
	"testing"
)

func TestFmtMatchTime(t *testing.T) {
	// UTC 2026-08-15T23:00:00 → 2026-08-16 06:00 WIB.
	got := fmtMatchTime("2026-08-15T23:00:00")
	if got != "06:00 WIB" {
		t.Errorf("konversi UTC→WIB salah: %q", got)
	}
	// Format tak dikenal → teks apa adanya.
	if got := fmtMatchTime("not-a-date"); got != "not-a-date" {
		t.Errorf("format tak dikenal harus dikembalikan apa adanya: %q", got)
	}
}

func TestFilterSportsDB(t *testing.T) {
	matches := []theSportsDBMatch{
		{League: "English Premier League", Home: "Arsenal", Away: "Chelsea", Time: "2026-08-15T14:00:00"},
		{League: "Indonesian League", Home: "Persija", Away: "Persib", Time: "2026-08-15T12:00:00"},
		{League: "Serie A", Home: "Inter", Away: "Milan", Time: "2026-08-15T18:45:00"},
	}
	leagues := []string{"English Premier League", "Indonesian League", "Serie A"}

	// Tanpa filter → semua.
	got, gotL := filterSportsDB(matches, leagues, "")
	if len(got) != 3 || len(gotL) != 3 {
		t.Fatalf("tanpa filter harus 3, dapat %d", len(got))
	}

	// Alias "inggris" → English Premier League.
	got, _ = filterSportsDB(matches, leagues, "inggris")
	if len(got) != 1 || got[0].Home != "Arsenal" {
		t.Errorf("filter 'inggris' salah: %+v", got)
	}

	// Nama tim langsung.
	got, _ = filterSportsDB(matches, leagues, "persija")
	if len(got) != 1 || got[0].League != "Indonesian League" {
		t.Errorf("filter 'persija' salah: %+v", got)
	}

	// Tidak cocok.
	got, _ = filterSportsDB(matches, leagues, "zzz")
	if len(got) != 0 {
		t.Errorf("filter 'zzz' harus kosong: %+v", got)
	}
}

func TestMatchLeagueEmoji(t *testing.T) {
	if got := matchLeagueEmoji("English Premier League 2026/27"); got != "🏴󠁧󠁢󠁥󠁮󠁧󠁿" {
		t.Errorf("emoji EPL salah: %q", got)
	}
	if got := matchLeagueEmoji("Some Obscure League"); got != "⚽" {
		t.Errorf("fallback harus ⚽: %q", got)
	}
}
