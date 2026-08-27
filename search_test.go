package main

import (
	"testing"
	"time"
)

// TestPinSessionLifecycle — store → lookup → TTL expiry (cleanup lazy).
func TestPinSessionLifecycle(t *testing.T) {
	chat := "628111@s.whatsapp.net"
	if s := lookupPinSession(chat); s != nil {
		t.Fatal("session baru harus nil")
	}

	pinSessions.Lock()
	pinSessions.m[chat] = &pinSession{
		results: []pinResult{{Title: "a", ImageURL: "u", Link: "l"}},
		ts:      time.Now(),
	}
	pinSessions.Unlock()

	s := lookupPinSession(chat)
	if s == nil || len(s.results) != 1 {
		t.Fatal("session harus ketemu setelah store")
	}

	// Paksa kedaluwarsa → lookup harus nil dan dihapus dari map.
	pinSessions.Lock()
	pinSessions.m[chat].ts = time.Now().Add(-pinSessionTTL - time.Minute)
	pinSessions.Unlock()

	if s := lookupPinSession(chat); s != nil {
		t.Fatal("session kedaluwarsa harus dianggap kosong")
	}
	pinSessions.Lock()
	_, exists := pinSessions.m[chat]
	pinSessions.Unlock()
	if exists {
		t.Fatal("session kedaluwarsa harus dihapus dari map")
	}
}

// TestPinPickerRows — format row dropdown: id `pinpick <idx>` 1-based,
// title bernomor, desc = link, title kosong → fallback query.
func TestPinPickerRows(t *testing.T) {
	rows := pinPickerRows("anime girl", "!", []pinResult{
		{Title: "Gambar A", ImageURL: "u1", Link: "l1"},
		{ImageURL: "u2", Link: "l2"}, // tanpa title → fallback query
		{Title: "Gambar C", ImageURL: "u3", Link: "l3"},
	})
	if len(rows) != 3 {
		t.Fatalf("jumlah row = %d, want 3", len(rows))
	}
	for i, want := range []string{"!pinpick 1", "!pinpick 2", "!pinpick 3"} {
		if rows[i].id != want {
			t.Errorf("row[%d].id = %q, want %q", i, rows[i].id, want)
		}
		if rows[i].desc != []string{"l1", "l2", "l3"}[i] {
			t.Errorf("row[%d].desc = %q, want link", i, rows[i].desc)
		}
	}
	if want := "1. Gambar A"; rows[0].title != want {
		t.Errorf("row[0].title = %q, want %q", rows[0].title, want)
	}
	if want := "2. anime girl"; rows[1].title != want {
		t.Errorf("row[1].title (fallback query) = %q, want %q", rows[1].title, want)
	}
}
