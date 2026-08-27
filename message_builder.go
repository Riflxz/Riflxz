package main

// message_builder.go — fluent builder untuk InteractiveMessage WhatsApp.
//
// Port dari MessageBuilderV4.6.js (NIXCODE) ke Go: pola chaining
// .SetHeader().SetBody().SetFooter().AddButton().Build() untuk menyusun
// pesan interaktif native flow dengan rapi dan konsisten — tanpa perlu
// menyentuh struct protobuf bertingkat setiap kali.
//
// Dua builder:
//   - MsgBuilder     — satu InteractiveMessage (quick_reply / single_select / cta_url)
//   - CarouselBuilder — InteractiveMessage_CarouselMessage: swipe horizontal
//     antar kartu, tiap kartu = satu MsgBuilder (port class Carousel).
//
// Semua versi menu (V1-V4) dan pesan interaktif lain sebaiknya lewat sini.

import (
	"context"
	"encoding/json"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ─── MsgBuilder — satu pesan interaktif native flow ─────────────────────────

type MsgBuilder struct {
	title    string
	subtitle string
	img      *menuImgCache // header media (image), nil = header teks
	body     string
	footer   string
	ctx      *waE2E.ContextInfo
	buttons  []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
}

func NewMsgBuilder() *MsgBuilder {
	return &MsgBuilder{}
}

// SetHeader — judul & subjudul header (teks, dipakai saat tanpa media).
func (b *MsgBuilder) SetHeader(title, subtitle string) *MsgBuilder {
	b.title = title
	b.subtitle = subtitle
	return b
}

// SetImageHeader — pakai gambar header (hasil getMenuImage, di-cache).
// img nil → header teks biasa.
func (b *MsgBuilder) SetImageHeader(img *menuImgCache) *MsgBuilder {
	b.img = img
	return b
}

func (b *MsgBuilder) SetBody(text string) *MsgBuilder {
	b.body = text
	return b
}

func (b *MsgBuilder) SetFooter(text string) *MsgBuilder {
	b.footer = text
	return b
}

func (b *MsgBuilder) SetContextInfo(ci *waE2E.ContextInfo) *MsgBuilder {
	b.ctx = ci
	return b
}

// AddQRButton — tombol quick_reply (kirim command saat diklik).
func (b *MsgBuilder) AddQRButton(label, id string) *MsgBuilder {
	b.buttons = append(b.buttons, qrButton(label, id))
	return b
}

// AddCTAURL — tombol link eksternal.
func (b *MsgBuilder) AddCTAURL(label, url string) *MsgBuilder {
	b.buttons = append(b.buttons, ctaURL(label, url))
	return b
}

// AddSelectButton — tombol single_select (dropdown list), port dari
// addSelection/makeSection/makeRow di MessageBuilder. Sekali panggil dengan
// []listSection (kategori + rows) — params JSON dirakit di sini.
func (b *MsgBuilder) AddSelectButton(title string, sections []listSection) *MsgBuilder {
	type rowParam struct {
		Header      string `json:"header"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ID          string `json:"id"`
	}
	type sectionParam struct {
		Title          string     `json:"title"`
		HighlightLabel string     `json:"highlight_label,omitempty"`
		Rows           []rowParam `json:"rows"`
	}
	type selectParams struct {
		Title    string         `json:"title"`
		Sections []sectionParam `json:"sections"`
	}

	var sps []sectionParam
	for _, sec := range sections {
		var rows []rowParam
		for _, r := range sec.rows {
			rows = append(rows, rowParam{
				Header:      r.header,
				Title:       r.title,
				Description: r.desc,
				ID:          r.id,
			})
		}
		sps = append(sps, sectionParam{Title: sec.title, Rows: rows})
	}
	paramsJSON, _ := json.Marshal(selectParams{Title: title, Sections: sps})
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("single_select"),
		ButtonParamsJSON: proto.String(string(paramsJSON)),
	})
	return b
}

// BuildCard — susun InteractiveMessage (tanpa wrapper). Dipakai langsung
// sebagai card carousel, atau dibungkus Build() untuk pesan biasa.
func (b *MsgBuilder) BuildCard() *waE2E.InteractiveMessage {
	header := buildImageHeader(context.Background(), b.title, b.subtitle)
	if b.img != nil {
		header = buildImageHeaderWith(b.img, b.title, b.subtitle)
	}
	return &waE2E.InteractiveMessage{
		Header:    header,
		Body:      &waE2E.InteractiveMessage_Body{Text: proto.String(b.body)},
		Footer:    &waE2E.InteractiveMessage_Footer{Text: proto.String(b.footer)},
		ContextInfo: b.ctx,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: b.buttons,
			},
		},
	}
}

// Build — bungkus card dalam ViewOnceMessage (format pesan interaktif biasa).
func (b *MsgBuilder) Build() *waE2E.Message {
	return &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				InteractiveMessage: b.BuildCard(),
			},
		},
	}
}

// Send — kirim pesan interaktif + node biz (biar bisa diklik di DM/grup).
// Return error asli; caller bebas memutuskan fallback.
func (b *MsgBuilder) Send(ctx context.Context, chat types.JID) error {
	nodes := buildBizNodes(chat)
	_, err := waClient.SendMessage(ctx, chat, b.Build(), whatsmeow.SendRequestExtra{
		AdditionalNodes: &nodes,
	})
	return err
}

// buildImageHeaderWith — sama seperti buildImageHeader tapi dengan media
// yang sudah di-cache (tidak perlu context download ulang).
func buildImageHeaderWith(img *menuImgCache, title, subtitle string) *waE2E.InteractiveMessage_Header {
	if img == nil {
		return &waE2E.InteractiveMessage_Header{
			Title:              proto.String(title),
			Subtitle:           proto.String(subtitle),
			HasMediaAttachment: proto.Bool(false),
		}
	}
	mime := img.mimetype
	if mime == "" {
		mime = "image/jpeg"
	}
	return &waE2E.InteractiveMessage_Header{
		Title:              proto.String(title),
		Subtitle:           proto.String(subtitle),
		HasMediaAttachment: proto.Bool(true),
		Media: &waE2E.InteractiveMessage_Header_ImageMessage{
			ImageMessage: &waE2E.ImageMessage{
				URL:           proto.String(img.up.URL),
				DirectPath:    proto.String(img.up.DirectPath),
				MediaKey:      img.up.MediaKey,
				FileEncSHA256: img.up.FileEncSHA256,
				FileSHA256:    img.up.FileSHA256,
				FileLength:    proto.Uint64(img.up.FileLength),
				Mimetype:      proto.String(mime),
			},
		},
	}
}

// ─── CarouselBuilder — swipe horizontal antar kartu (port class Carousel) ───

// carouselMaxCards — batas kartu yang diterima WhatsApp (dari implementasi
// resmi & pengalaman komunitas: 10).
const carouselMaxCards = 10

type CarouselBuilder struct {
	cards []*waE2E.InteractiveMessage
}

func NewCarouselBuilder() *CarouselBuilder {
	return &CarouselBuilder{}
}

// AddCard — tambah satu kartu (card harus punya header media — WhatsApp
// menolak carousel tanpa media). Mengembalikan error bila batas kartu
// terlampaui.
func (cb *CarouselBuilder) AddCard(card *MsgBuilder) error {
	if len(cb.cards) >= carouselMaxCards {
		return fmt.Errorf("carousel maksimal %d kartu", carouselMaxCards)
	}
	im := card.BuildCard()
	if !im.GetHeader().GetHasMediaAttachment() {
		return fmt.Errorf("kartu carousel wajib punya header media (gambar)")
	}
	cb.cards = append(cb.cards, im)
	return nil
}

func (cb *CarouselBuilder) Len() int { return len(cb.cards) }

// Build — bungkus carousel dalam ViewOnceMessage + InteractiveMessage.
func (cb *CarouselBuilder) Build() *waE2E.Message {
	return &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				InteractiveMessage: &waE2E.InteractiveMessage{
					InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{
						CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
							Cards:            cb.cards,
							MessageVersion:   proto.Int32(1),
							CarouselCardType: waE2E.InteractiveMessage_CarouselMessage_HSCROLL_CARDS.Enum(),
						},
					},
				},
			},
		},
	}
}

// Send — kirim carousel + node biz.
func (cb *CarouselBuilder) Send(ctx context.Context, chat types.JID) error {
	nodes := buildBizNodes(chat)
	_, err := waClient.SendMessage(ctx, chat, cb.Build(), whatsmeow.SendRequestExtra{
		AdditionalNodes: &nodes,
	})
	return err
}
