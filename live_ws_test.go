package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveAlightWS — uji live alur persis generator.js asli:
// gen email baru → POST send-link → WebSocket notificon + alightCheckInbox
// (num_mess → extract link) → aktifasi.
func TestLiveAlightWS(t *testing.T) {
	if testing.Short() || os.Getenv("LIVE_ALIGHT_TEST") == "" {
		t.Skip("skip live test (set LIVE_ALIGHT_TEST=1 untuk jalankan)")
	}
	email := alightGenEmail()
	user := strings.SplitN(email, "@", 2)[0]

	if _, err := alightPost(mtzSendLink, map[string]string{"email": email}); err != nil {
		t.Fatalf("send-link: %v", err)
	}
	t.Logf("send-link OK, email=%s menunggu magic link...", email)

	start := time.Now()
	link, err := alightWaitInboxLink(user)
	if err != nil {
		t.Fatalf("inbox: %v (durasi %s)", err, time.Since(start).Round(time.Second))
	}
	t.Logf("LINK1 ditemukan (%s): %s", time.Since(start).Round(time.Second), link)

	if _, err := alightPost(mtzAktifasi, map[string]string{"email": email, "link": link}); err != nil {
		t.Fatalf("aktifasi: %v", err)
	}
	t.Logf("AKUN AKTIF PREMIUM — email=%s", email)

	// Tahap 2: baseline link, lalu cek alightWaitNewInboxLink (WS juga menyala).
	baselineLink := alightBaselineLink(user)
	t.Logf("baseline link: %q", baselineLink)
	// Jangan tunggu 5 menit di test; cek WS 30 detik apakah msgID datang
	// (tidak ada login user, jadi WS harusnya diam — ini cuma sanity check).
	wsCh := alightWaitOobWS(user, 30*time.Second)
	select {
	case id, ok := <-wsCh:
		if ok {
			t.Logf("WS kasih msgID: %s (tidak ada login, kemungkinan email masuk?!)", id)
		}
	case <-time.After(30 * time.Second):
		t.Log("WS 30s tidak dapat msgID (wajar, user belum login)")
	}
}
