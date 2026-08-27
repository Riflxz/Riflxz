package main

// message_builder_test.go — verifikasi struktur proto yang dihasilkan
// MsgBuilder & CarouselBuilder (tanpa koneksi WhatsApp).

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

// fakeMenuImg — UploadResponse tiruan; hanya struktur yang dicek, bukan nilai.
func fakeMenuImg() *menuImgCache {
	return &menuImgCache{
		up: &whatsmeow.UploadResponse{
			URL:        "https://example.com/img.jpg",
			DirectPath: "/dummy/direct_path",
			MediaKey:   []byte("0123456789abcdef0123456789abcdef"),
			FileSHA256: []byte("s"),
			FileLength: 1234,
		},
		mimetype: "image/jpeg",
	}
}

func unwrapInteractive(t *testing.T, m *waE2E.Message) *waE2E.InteractiveMessage {
	t.Helper()
	im := m.GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	if im == nil {
		t.Fatal("InteractiveMessage tidak ditemukan di ViewOnceMessage")
	}
	return im
}

// TestMsgBuilderSelectJSON — tombol single_select harus membawa params JSON
// yang valid (title + sections + rows) sesuai kontrak native flow.
func TestMsgBuilderSelectJSON(t *testing.T) {
	b := NewMsgBuilder().
		SetHeader("Menu", "by dev").
		SetImageHeader(fakeMenuImg()).
		SetBody("pilih di bawah").
		AddSelectButton("Pilih Command", []listSection{
			{title: "UMUM", rows: []listRow{
				{header: "UMUM", title: "menu", desc: "daftar menu", id: "!menu"},
			}},
		}).
		AddQRButton("Menu", "!menu")

	im := unwrapInteractive(t, b.Build())
	if !im.GetHeader().GetHasMediaAttachment() {
		t.Error("header media harus true setelah SetImageHeader")
	}
	if im.GetHeader().GetImageMessage() == nil {
		t.Error("ImageMessage tidak terpasang di header")
	}

	nfb := im.GetNativeFlowMessage()
	if nfb == nil {
		t.Fatal("NativeFlowMessage tidak ada")
	}
	if len(nfb.GetButtons()) != 2 {
		t.Fatalf("harus ada 2 tombol, ada %d", len(nfb.GetButtons()))
	}
	sel := nfb.GetButtons()[0]
	if sel.GetName() != "single_select" {
		t.Fatalf("tombol pertama harus single_select, dapat %q", sel.GetName())
	}
	var params struct {
		Title    string `json:"title"`
		Sections []struct {
			Title string `json:"title"`
			Rows  []struct {
				Header string `json:"header"`
				Title  string `json:"title"`
				ID     string `json:"id"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(sel.GetButtonParamsJSON()), &params); err != nil {
		t.Fatalf("ButtonParamsJSON tidak valid JSON: %v", err)
	}
	if params.Title != "Pilih Command" || len(params.Sections) != 1 || len(params.Sections[0].Rows) != 1 {
		t.Fatalf("params tidak sesuai: %+v", params)
	}
	if params.Sections[0].Rows[0].ID != "!menu" {
		t.Fatalf("row id tidak sesuai: %+v", params.Sections[0].Rows[0])
	}

	qr := nfb.GetButtons()[1]
	if qr.GetName() != "quick_reply" {
		t.Fatalf("tombol kedua harus quick_reply, dapat %q", qr.GetName())
	}
}

// TestCarouselBuilderRules — kartu tanpa media ditolak; batas 10 kartu.
func TestCarouselBuilderRules(t *testing.T) {
	img := fakeMenuImg()

	cb := NewCarouselBuilder()
	// Kartu tanpa image header → harus ditolak.
	if err := cb.AddCard(NewMsgBuilder().SetHeader("a", "b")); err == nil {
		t.Error("kartu tanpa media harus ditolak")
	}
	// Kartu dengan image header → diterima, hingga 10.
	for i := 0; i < carouselMaxCards; i++ {
		if err := cb.AddCard(NewMsgBuilder().SetHeader("a", "b").SetImageHeader(img)); err != nil {
			t.Fatalf("kartu ke-%d ditolak: %v", i, err)
		}
	}
	if cb.Len() != carouselMaxCards {
		t.Fatalf("harus ada %d kartu, ada %d", carouselMaxCards, cb.Len())
	}
	if err := cb.AddCard(NewMsgBuilder().SetHeader("a", "b").SetImageHeader(img)); err == nil {
		t.Error("kartu ke-11 harus ditolak (batas 10)")
	}
}

// TestCarouselBuilderBuild — struktur proto carousel: ViewOnceMessage →
// InteractiveMessage → CarouselMessage, tiap kartu punya header media.
func TestCarouselBuilderBuild(t *testing.T) {
	img := fakeMenuImg()
	cb := NewCarouselBuilder()
	for i := 0; i < 2; i++ {
		card := NewMsgBuilder().
			SetHeader("Kartu", "sub").
			SetImageHeader(img).
			SetBody("isi").
			AddQRButton("Menu", "!menu")
		if err := cb.AddCard(card); err != nil {
			t.Fatalf("AddCard gagal: %v", err)
		}
	}

	im := unwrapInteractive(t, cb.Build())
	cm := im.GetCarouselMessage()
	if cm == nil {
		t.Fatal("CarouselMessage tidak ada")
	}
	if len(cm.GetCards()) != 2 {
		t.Fatalf("harus 2 kartu, ada %d", len(cm.GetCards()))
	}
	for i, card := range cm.GetCards() {
		if !card.GetHeader().GetHasMediaAttachment() {
			t.Errorf("kartu %d: header media harus true", i)
		}
		if card.GetNativeFlowMessage() == nil || len(card.GetNativeFlowMessage().GetButtons()) != 1 {
			t.Errorf("kartu %d: harus punya 1 tombol", i)
		}
	}
	if cm.GetCarouselCardType() != waE2E.InteractiveMessage_CarouselMessage_HSCROLL_CARDS {
		t.Errorf("carousel_card_type harus HSCROLL_CARDS, dapat %v", cm.GetCarouselCardType())
	}
	if cm.GetMessageVersion() != 1 {
		t.Errorf("message_version harus 1, dapat %d", cm.GetMessageVersion())
	}
}

// TestMsgBuilderTextHeader — tanpa gambar, header harus teks + has_media false.
func TestMsgBuilderTextHeader(t *testing.T) {
	b := NewMsgBuilder().SetHeader("T", "S").SetBody("b")
	im := unwrapInteractive(t, b.Build())
	h := im.GetHeader()
	if h.GetHasMediaAttachment() {
		t.Error("tanpa gambar, has_media_attachment harus false")
	}
	if h.GetTitle() != "T" || h.GetSubtitle() != "S" {
		t.Errorf("title/subtitle tidak sesuai: %q / %q", h.GetTitle(), h.GetSubtitle())
	}
}
