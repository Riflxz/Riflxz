package main

// MessageBuilder — port Go dari NIXCODE MessageBuilderV4.6 (Baileys) ke whatsmeow.
// Struktur mengikuti library asli: BaseBuilder + Button (native flow) +
// ButtonV2 (buttons legacy) + Carousel + AIRich (rich response GenAI).
//
// Referensi: https://gist.github.com/ValdazGT/ce6532c1d4ff192bb718f1acb392d460

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// BaseBuilder — state bersama untuk semua builder
// ---------------------------------------------------------------------------

type BaseBuilder struct {
	title        string
	subtitle     string
	body         string
	footer       string
	contextInfo  *waE2E.ContextInfo
	extraPayload map[string]any
}

func NewBaseBuilder() *BaseBuilder {
	return &BaseBuilder{extraPayload: map[string]any{}}
}

func (b *BaseBuilder) SetTitle(title string) *BaseBuilder {
	b.title = title
	return b
}

func (b *BaseBuilder) SetSubtitle(subtitle string) *BaseBuilder {
	b.subtitle = subtitle
	return b
}

func (b *BaseBuilder) SetBody(body string) *BaseBuilder {
	b.body = body
	return b
}

func (b *BaseBuilder) SetFooter(footer string) *BaseBuilder {
	b.footer = footer
	return b
}

func (b *BaseBuilder) SetContextInfo(ci *waE2E.ContextInfo) *BaseBuilder {
	b.contextInfo = ci
	return b
}

func (b *BaseBuilder) AddPayload(key string, val any) *BaseBuilder {
	b.extraPayload[key] = val
	return b
}

// ---------------------------------------------------------------------------
// Button — native flow buttons (setara class Button di JS)
// ---------------------------------------------------------------------------

type Button struct {
	*BaseBuilder
	buttons []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton
	params  string
	media   *waE2E.Message // header media (image/video/document)
}

func NewButton() *Button {
	return &Button{BaseBuilder: NewBaseBuilder()}
}

// Override fluent chaining agar mengembalikan *Button (bukan *BaseBuilder).
func (b *Button) SetTitle(t string) *Button    { b.BaseBuilder.SetTitle(t); return b }
func (b *Button) SetSubtitle(s string) *Button { b.BaseBuilder.SetSubtitle(s); return b }
func (b *Button) SetBody(s string) *Button     { b.BaseBuilder.SetBody(s); return b }
func (b *Button) SetFooter(s string) *Button   { b.BaseBuilder.SetFooter(s); return b }
func (b *Button) SetContextInfo(ci *waE2E.ContextInfo) *Button {
	b.BaseBuilder.SetContextInfo(ci)
	return b
}
func (b *Button) AddPayload(k string, v any) *Button { b.BaseBuilder.AddPayload(k, v); return b }

// AddReply — tombol quick reply (name: quick_reply)
func (b *Button) AddReply(displayText, id string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("quick_reply"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"id":%q}`, displayText, id)),
	})
	return b
}

// AddCall — tombol panggil (name: cta_call)
func (b *Button) AddCall(displayText, id string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("cta_call"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"id":%q}`, displayText, id)),
	})
	return b
}

// AddURL — tombol buka URL (name: cta_url)
func (b *Button) AddURL(displayText, url string, webviewInteraction bool) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("cta_url"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"url":%q,"webview_interaction":%v}`,
			displayText, url, webviewInteraction)),
	})
	return b
}

// AddCopy — tombol salin kode (name: cta_copy)
func (b *Button) AddCopy(displayText, copyCode string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("cta_copy"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"copy_code":%q}`, displayText, copyCode)),
	})
	return b
}

// AddLocation — tombol kirim lokasi (name: send_location)
func (b *Button) AddLocation() *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("send_location"),
		ButtonParamsJSON: proto.String(`{}`),
	})
	return b
}

// AddReminder — tombol buat pengingat (name: cta_reminder)
func (b *Button) AddReminder(displayText, id string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("cta_reminder"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"id":%q}`, displayText, id)),
	})
	return b
}

// AddAddress — tombol kirim alamat (name: address_message)
func (b *Button) AddAddress(displayText, id string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("address_message"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"display_text":%q,"id":%q}`, displayText, id)),
	})
	return b
}

// AddSelection — mulai tombol single_select; lanjutkan dengan MakeSection/MakeRow
func (b *Button) AddSelection(title string) *Button {
	b.buttons = append(b.buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name: proto.String("single_select"),
		ButtonParamsJSON: proto.String(fmt.Sprintf(
			`{"title":%q,"sections":[]}`, title)),
	})
	return b
}

// MakeSection — tambah section ke single_select terakhir
func (b *Button) MakeSection(title, highlightLabel string) *Button {
	if len(b.buttons) == 0 {
		return b
	}
	last := b.buttons[len(b.buttons)-1]
	var params struct {
		Title    string `json:"title"`
		Sections []any  `json:"sections"`
	}
	_ = json.Unmarshal([]byte(last.GetButtonParamsJSON()), &params)
	params.Sections = append(params.Sections, map[string]any{
		"title":           title,
		"highlight_label": highlightLabel,
		"rows":            []any{},
	})
	data, _ := json.Marshal(params)
	last.ButtonParamsJSON = proto.String(string(data))
	return b
}

// MakeRow — tambah row ke section terakhir dari single_select terakhir
func (b *Button) MakeRow(header, title, description, id string) *Button {
	if len(b.buttons) == 0 {
		return b
	}
	last := b.buttons[len(b.buttons)-1]
	var params struct {
		Title    string `json:"title"`
		Sections []struct {
			Title          string `json:"title"`
			HighlightLabel string `json:"highlight_label"`
			Rows           []any  `json:"rows"`
		} `json:"sections"`
	}
	_ = json.Unmarshal([]byte(last.GetButtonParamsJSON()), &params)
	if len(params.Sections) == 0 {
		return b
	}
	sec := &params.Sections[len(params.Sections)-1]
	sec.Rows = append(sec.Rows, map[string]any{
		"header":      header,
		"title":       title,
		"description": description,
		"id":          id,
	})
	data, _ := json.Marshal(params)
	last.ButtonParamsJSON = proto.String(string(data))
	return b
}

// SetParams — messageParamsJson untuk native flow (mis. bottom_sheet)
func (b *Button) SetParams(params string) *Button {
	b.params = params
	return b
}

// SetImage — media header (URL)
func (b *Button) SetImage(url string) *Button {
	b.media = &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{URL: proto.String(url)},
	}
	return b
}

// SetVideo — media header (URL)
func (b *Button) SetVideo(url string) *Button {
	b.media = &waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{URL: proto.String(url)},
	}
	return b
}

// SetDocument — media header (URL)
func (b *Button) SetDocument(url, filename string) *Button {
	b.media = &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:      proto.String(url),
			FileName: proto.String(filename),
		},
	}
	return b
}

func (b *Button) Build() *waE2E.Message {
	header := &waE2E.InteractiveMessage_Header{
		Title:              proto.String(b.title),
		Subtitle:           proto.String(b.subtitle),
		HasMediaAttachment: proto.Bool(b.media != nil),
	}
	if b.media != nil {
		switch {
		case b.media.ImageMessage != nil:
			header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: b.media.ImageMessage}
		case b.media.VideoMessage != nil:
			header.Media = &waE2E.InteractiveMessage_Header_VideoMessage{VideoMessage: b.media.VideoMessage}
		case b.media.DocumentMessage != nil:
			header.Media = &waE2E.InteractiveMessage_Header_DocumentMessage{DocumentMessage: b.media.DocumentMessage}
		}
	}

	return &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header:      header,
			Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(b.body)},
			Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(b.footer)},
			ContextInfo: b.contextInfo,
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					MessageParamsJSON: proto.String(b.params),
					Buttons:           b.buttons,
				},
			},
		},
	}
}

func (b *Button) Send(ctx context.Context, chat types.JID) (types.MessageID, error) {
	if len(b.buttons) == 0 {
		return "", fmt.Errorf("Button requires at least one button")
	}
	resp, err := waClient.SendMessage(ctx, chat, b.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// ButtonV2 — legacy buttonsMessage (setara class ButtonV2 di JS)
// ---------------------------------------------------------------------------

type ButtonV2 struct {
	*BaseBuilder
	buttons   []*waE2E.ButtonsMessage_Button
	thumbnail string // URL gambar thumbnail
	media     *waE2E.Message
}

func NewButtonV2() *ButtonV2 {
	return &ButtonV2{BaseBuilder: NewBaseBuilder()}
}

// Override fluent chaining agar mengembalikan *ButtonV2.
func (b *ButtonV2) SetTitle(t string) *ButtonV2    { b.BaseBuilder.SetTitle(t); return b }
func (b *ButtonV2) SetSubtitle(s string) *ButtonV2 { b.BaseBuilder.SetSubtitle(s); return b }
func (b *ButtonV2) SetBody(s string) *ButtonV2     { b.BaseBuilder.SetBody(s); return b }
func (b *ButtonV2) SetFooter(s string) *ButtonV2   { b.BaseBuilder.SetFooter(s); return b }
func (b *ButtonV2) SetContextInfo(ci *waE2E.ContextInfo) *ButtonV2 {
	b.BaseBuilder.SetContextInfo(ci)
	return b
}
func (b *ButtonV2) AddPayload(k string, v any) *ButtonV2 { b.BaseBuilder.AddPayload(k, v); return b }

func (b *ButtonV2) AddButton(displayText, buttonID string) *ButtonV2 {
	if buttonID == "" {
		buttonID = fmt.Sprintf("btn-%d", time.Now().UnixNano())
	}
	b.buttons = append(b.buttons, &waE2E.ButtonsMessage_Button{
		ButtonID: proto.String(buttonID),
		ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{
			DisplayText: proto.String(displayText),
		},
		Type: waE2E.ButtonsMessage_Button_RESPONSE.Enum(),
	})
	return b
}

func (b *ButtonV2) SetThumbnail(url string) *ButtonV2 {
	b.thumbnail = url
	return b
}

func (b *ButtonV2) SetMedia(msg *waE2E.Message) *ButtonV2 {
	b.media = msg
	return b
}

func (b *ButtonV2) Build() *waE2E.Message {
	bm := &waE2E.ButtonsMessage{
		ContentText: proto.String(b.body),
		FooterText:  proto.String(b.footer),
		ContextInfo: b.contextInfo,
		Buttons:     b.buttons,
	}

	switch {
	case b.media != nil && b.media.ImageMessage != nil:
		bm.Header = &waE2E.ButtonsMessage_ImageMessage{ImageMessage: b.media.ImageMessage}
		bm.HeaderType = waE2E.ButtonsMessage_IMAGE.Enum()
	case b.media != nil && b.media.VideoMessage != nil:
		bm.Header = &waE2E.ButtonsMessage_VideoMessage{VideoMessage: b.media.VideoMessage}
		bm.HeaderType = waE2E.ButtonsMessage_VIDEO.Enum()
	case b.media != nil && b.media.DocumentMessage != nil:
		bm.Header = &waE2E.ButtonsMessage_DocumentMessage{DocumentMessage: b.media.DocumentMessage}
		bm.HeaderType = waE2E.ButtonsMessage_DOCUMENT.Enum()
	case b.thumbnail != "":
		// Header lokasi dengan thumbnail (pola JS: headerType 6 + locationMessage)
		bm.Header = &waE2E.ButtonsMessage_LocationMessage{
			LocationMessage: &waE2E.LocationMessage{
				DegreesLatitude:  proto.Float64(0),
				DegreesLongitude: proto.Float64(0),
				Name:             proto.String(b.title),
				Address:          proto.String(b.subtitle),
				JPEGThumbnail:    []byte(b.thumbnail),
			},
		}
		bm.HeaderType = waE2E.ButtonsMessage_LOCATION.Enum()
	default:
		bm.HeaderType = waE2E.ButtonsMessage_EMPTY.Enum()
	}

	return &waE2E.Message{ButtonsMessage: bm}
}

func (b *ButtonV2) Send(ctx context.Context, chat types.JID) (types.MessageID, error) {
	if len(b.buttons) == 0 {
		return "", fmt.Errorf("ButtonV2 requires at least one button")
	}
	resp, err := waClient.SendMessage(ctx, chat, b.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// Carousel — carouselMessage (setara class Carousel di JS)
// ---------------------------------------------------------------------------

type Carousel struct {
	*BaseBuilder
	cards []*waE2E.InteractiveMessage
}

func NewCarousel() *Carousel {
	return &Carousel{BaseBuilder: NewBaseBuilder()}
}

// Override fluent chaining agar mengembalikan *Carousel.
func (c *Carousel) SetTitle(t string) *Carousel    { c.BaseBuilder.SetTitle(t); return c }
func (c *Carousel) SetSubtitle(s string) *Carousel { c.BaseBuilder.SetSubtitle(s); return c }
func (c *Carousel) SetBody(s string) *Carousel     { c.BaseBuilder.SetBody(s); return c }
func (c *Carousel) SetFooter(s string) *Carousel   { c.BaseBuilder.SetFooter(s); return c }
func (c *Carousel) SetContextInfo(ci *waE2E.ContextInfo) *Carousel {
	c.BaseBuilder.SetContextInfo(ci)
	return c
}
func (c *Carousel) AddPayload(k string, v any) *Carousel { c.BaseBuilder.AddPayload(k, v); return c }

func (c *Carousel) AddCard(card *waE2E.InteractiveMessage) *Carousel {
	c.cards = append(c.cards, card)
	return c
}

func (c *Carousel) Build() *waE2E.Message {
	return &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Header:      &waE2E.InteractiveMessage_Header{HasMediaAttachment: proto.Bool(false)},
			Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(c.body)},
			Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(c.footer)},
			ContextInfo: c.contextInfo,
			InteractiveMessage: &waE2E.InteractiveMessage_CarouselMessage_{
				CarouselMessage: &waE2E.InteractiveMessage_CarouselMessage{
					Cards:            c.cards,
					MessageVersion:   proto.Int32(1),
					CarouselCardType: waE2E.InteractiveMessage_CarouselMessage_HSCROLL_CARDS.Enum(),
				},
			},
		},
	}
}

func (c *Carousel) Send(ctx context.Context, chat types.JID) (types.MessageID, error) {
	if len(c.cards) == 0 {
		return "", fmt.Errorf("Carousel requires at least one card")
	}
	resp, err := waClient.SendMessage(ctx, chat, c.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// AIRich — rich response GenAI (setara class AIRich di JS)
// ---------------------------------------------------------------------------

type AIRich struct {
	*BaseBuilder
	submessages []*waAICommonDeprecated.AIRichResponseSubMessage
	sections    []any
	sources     []*waAICommon.BotSourcesMetadata_BotSourceItem
}

func NewAIRich() *AIRich {
	return &AIRich{BaseBuilder: NewBaseBuilder()}
}

// Override fluent chaining agar mengembalikan *AIRich.
func (a *AIRich) SetTitle(t string) *AIRich    { a.BaseBuilder.SetTitle(t); return a }
func (a *AIRich) SetSubtitle(s string) *AIRich { a.BaseBuilder.SetSubtitle(s); return a }
func (a *AIRich) SetBody(s string) *AIRich     { a.BaseBuilder.SetBody(s); return a }
func (a *AIRich) SetFooter(s string) *AIRich   { a.BaseBuilder.SetFooter(s); return a }
func (a *AIRich) SetContextInfo(ci *waE2E.ContextInfo) *AIRich {
	a.BaseBuilder.SetContextInfo(ci)
	return a
}
func (a *AIRich) AddPayload(k string, v any) *AIRich { a.BaseBuilder.AddPayload(k, v); return a }

// newLayout — view_model GenAI (Single = primitive, HScroll = primitives)
func newLayout(name string, data any) map[string]any {
	vm := map[string]any{"__typename": "GenAI" + name + "LayoutViewModel"}
	if arr, ok := data.([]any); ok {
		vm["primitives"] = arr
	} else {
		vm["primitive"] = data
	}
	return map[string]any{"view_model": vm}
}

// AddText — blok teks markdown (GenAIMarkdownTextUXPrimitive)
func (a *AIRich) AddText(text string) *AIRich {
	a.submessages = append(a.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	a.sections = append(a.sections, newLayout("Single", map[string]any{
		"text":       text,
		"__typename": "GenAIMarkdownTextUXPrimitive",
	}))
	return a
}

// AddCode — blok kode dengan highlight (GenAICodeUXPrimitive)
func (a *AIRich) AddCode(language, code string) *AIRich {
	blocks := tokenizeCode(code, language)

	a.submessages = append(a.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_CODE.Enum(),
		CodeMetadata: &waAICommonDeprecated.AIRichResponseCodeMetadata{
			CodeLanguage: proto.String(language),
			CodeBlocks:   blocks,
		},
	})
	a.sections = append(a.sections, newLayout("Single", map[string]any{
		"language":    language,
		"code_blocks": unifiedCodeBlocks(blocks),
		"__typename":  "GenAICodeUXPrimitive",
	}))
	return a
}

// AddTable — tabel asli WhatsApp (GenATableUXPrimitive).
// Input: [][]string, baris pertama = header.
func (a *AIRich) AddTable(table [][]string) *AIRich {
	if len(table) == 0 {
		return a
	}
	header := table[0]
	rows := table[1:]
	maxLen := len(header)
	for _, r := range rows {
		if len(r) > maxLen {
			maxLen = len(r)
		}
	}
	normalize := func(r []string) []string {
		out := make([]string, maxLen)
		copy(out, r)
		return out
	}

	// Submessage: tableMetadata
	var metaRows []*waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow
	metaRows = append(metaRows, &waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow{
		Items:     normalize(header),
		IsHeading: proto.Bool(true),
	})
	for _, r := range rows {
		metaRows = append(metaRows, &waAICommonDeprecated.AIRichResponseTableMetadata_AIRichResponseTableRow{
			Items:     normalize(r),
			IsHeading: proto.Bool(false),
		})
	}
	a.submessages = append(a.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TABLE.Enum(),
		TableMetadata: &waAICommonDeprecated.AIRichResponseTableMetadata{
			Title: proto.String(""),
			Rows:  metaRows,
		},
	})

	// Section: unified rows
	var unifiedRows []any
	unifiedRows = append(unifiedRows, map[string]any{
		"is_header": true,
		"cells":     normalize(header),
	})
	for _, r := range rows {
		unifiedRows = append(unifiedRows, map[string]any{
			"is_header": false,
			"cells":     normalize(r),
		})
	}
	a.sections = append(a.sections, newLayout("Single", map[string]any{
		"rows":       unifiedRows,
		"__typename": "GenATableUXPrimitive",
	}))
	return a
}

// AddSource — sumber referensi (GenAISearchResultPrimitive).
// Input: [][3]string {iconURL, url, displayName}
func (a *AIRich) AddSource(sources [][3]string) *AIRich {
	var items []any
	for i, s := range sources {
		icon, url, name := s[0], s[1], s[2]
		items = append(items, map[string]any{
			"source_type":         "THIRD_PARTY",
			"source_display_name": name,
			"source_subtitle":     "AI",
			"source_url":          url,
			"favicon": map[string]any{
				"url":       icon,
				"mime_type": "image/jpeg",
				"width":     16,
				"height":    16,
			},
		})
		a.sources = append(a.sources, &waAICommon.BotSourcesMetadata_BotSourceItem{
			Provider:          waAICommon.BotSourcesMetadata_BotSourceItem_OTHER.Enum(),
			ThumbnailCDNURL:   proto.String(icon),
			SourceProviderURL: proto.String(url),
			SourceQuery:       proto.String(""),
			FaviconCDNURL:     proto.String(icon),
			CitationNumber:    proto.Uint32(uint32(i + 1)),
			SourceTitle:       proto.String(name),
		})
	}
	a.sections = append(a.sections, newLayout("Single", map[string]any{
		"sources":    items,
		"__typename": "GenAISearchResultPrimitive",
	}))
	return a
}

// AddTip — teks metadata kecil (GenAIMetadataTextPrimitive)
func (a *AIRich) AddTip(text string) *AIRich {
	a.submessages = append(a.submessages, &waAICommonDeprecated.AIRichResponseSubMessage{
		MessageType: waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT.Enum(),
		MessageText: proto.String(text),
	})
	a.sections = append(a.sections, newLayout("Single", map[string]any{
		"text":       text,
		"__typename": "GenAIMetadataTextPrimitive",
	}))
	return a
}

// AddSuggest — saran follow-up (GenAIFollowUpSuggestionPillPrimitive)
func (a *AIRich) AddSuggest(suggestions []string) *AIRich {
	var pills []any
	for _, s := range suggestions {
		pills = append(pills, map[string]any{
			"prompt_text": s,
			"prompt_type": "SUGGESTED_PROMPT",
			"__typename":  "GenAIFollowUpSuggestionPillPrimitive",
		})
	}
	a.sections = append(a.sections, newLayout("HScroll", pills))
	return a
}

func (a *AIRich) Build() *waE2E.Message {
	sections := a.sections
	if a.footer != "" {
		sections = append(sections, newLayout("Single", map[string]any{
			"text":       a.footer,
			"__typename": "GenAIMetadataTextPrimitive",
		}))
	}

	unified := map[string]any{
		"response_id": fmt.Sprintf("resp-%d", time.Now().UnixNano()),
		"sections":    sections,
	}
	data, _ := json.Marshal(unified)
	encoded := base64.StdEncoding.EncodeToString(data)

	ci := &waE2E.ContextInfo{
		ForwardingScore: proto.Uint32(1),
		IsForwarded:     proto.Bool(true),
		ForwardedAiBotMessageInfo: &waAICommon.ForwardedAIBotMessageInfo{
			BotJID: proto.String("0@bot"),
		},
		ForwardOrigin: waE2E.ContextInfo_META_AI.Enum(),
	}
	if a.contextInfo != nil {
		ci = a.contextInfo
		if ci.ForwardingScore == nil {
			ci.ForwardingScore = proto.Uint32(1)
		}
		if ci.IsForwarded == nil {
			ci.IsForwarded = proto.Bool(true)
		}
		if ci.ForwardedAiBotMessageInfo == nil {
			ci.ForwardedAiBotMessageInfo = &waAICommon.ForwardedAIBotMessageInfo{BotJID: proto.String("0@bot")}
		}
		if ci.ForwardOrigin == nil {
			ci.ForwardOrigin = waE2E.ContextInfo_META_AI.Enum()
		}
	}

	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
			BotMetadata: &waAICommon.BotMetadata{
				MessageDisclaimerText: proto.String(a.title),
				RichResponseSourcesMetadata: &waAICommon.BotSourcesMetadata{
					Sources: a.sources,
				},
			},
		},
		BotForwardedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				RichResponseMessage: &waE2E.AIRichResponseMessage{
					MessageType: waAICommonDeprecated.AIRichResponseMessageType_AI_RICH_RESPONSE_TYPE_STANDARD.Enum(),
					Submessages: a.submessages,
					UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
						Data: []byte(encoded),
					},
					ContextInfo: ci,
				},
			},
		},
	}
}

func (a *AIRich) Send(ctx context.Context, chat types.JID) (types.MessageID, error) {
	if len(a.submessages) == 0 && len(a.sections) == 0 {
		return "", fmt.Errorf("AIRich requires at least one content block")
	}
	resp, err := waClient.SendMessage(ctx, chat, a.Build())
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ---------------------------------------------------------------------------
// tokenizeCode — tokenizer sederhana untuk highlight kode (port dari JS)
// ---------------------------------------------------------------------------

var codeKeywords = map[string]map[string]bool{
	"go": {
		"break": true, "case": true, "chan": true, "const": true, "continue": true,
		"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
		"func": true, "go": true, "goto": true, "if": true, "import": true,
		"interface": true, "map": true, "package": true, "range": true, "return": true,
		"select": true, "struct": true, "switch": true, "type": true, "var": true,
	},
	"javascript": {
		"break": true, "case": true, "catch": true, "class": true, "const": true,
		"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
		"else": true, "export": true, "extends": true, "finally": true, "for": true,
		"function": true, "if": true, "import": true, "in": true, "instanceof": true,
		"let": true, "new": true, "return": true, "static": true, "super": true,
		"switch": true, "this": true, "throw": true, "try": true, "typeof": true,
		"var": true, "void": true, "while": true, "with": true, "yield": true,
		"true": true, "false": true, "null": true, "undefined": true, "async": true,
		"await": true,
	},
	"python": {
		"False": true, "None": true, "True": true, "and": true, "as": true,
		"assert": true, "async": true, "await": true, "break": true, "class": true,
		"continue": true, "def": true, "del": true, "elif": true, "else": true,
		"except": true, "finally": true, "for": true, "from": true, "global": true,
		"if": true, "import": true, "in": true, "is": true, "lambda": true,
		"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
		"return": true, "try": true, "while": true, "with": true, "yield": true,
	},
}

const (
	codeTypeDefault = 0
	codeTypeKeyword = 1
	codeTypeMethod  = 2
	codeTypeString  = 3
	codeTypeNumber  = 4
	codeTypeComment = 5
)

func tokenizeCode(code, lang string) []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock {
	keywords := codeKeywords[strings.ToLower(lang)]
	if keywords == nil {
		return []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{{
			HighlightType: waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_DEFAULT.Enum(),
			CodeContent:   proto.String(code),
		}}
	}

	var tokens []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock
	push := func(content string, typ waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType) {
		if content == "" {
			return
		}
		if n := len(tokens); n > 0 && tokens[n-1].GetHighlightType() == typ {
			tokens[n-1].CodeContent = proto.String(tokens[n-1].GetCodeContent() + content)
			return
		}
		tokens = append(tokens, &waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{
			HighlightType: typ.Enum(),
			CodeContent:   proto.String(content),
		})
	}

	typOf := func(t int) waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType {
		switch t {
		case codeTypeKeyword:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_KEYWORD
		case codeTypeMethod:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_METHOD
		case codeTypeString:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_STRING
		case codeTypeNumber:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_NUMBER
		case codeTypeComment:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_COMMENT
		default:
			return waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_DEFAULT
		}
	}

	i := 0
	for i < len(code) {
		c := code[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s := i
			for i < len(code) && (code[i] == ' ' || code[i] == '\t' || code[i] == '\n' || code[i] == '\r') {
				i++
			}
			push(code[s:i], typOf(codeTypeDefault))
		case c == '/' && i+1 < len(code) && code[i+1] == '/':
			s := i
			for i < len(code) && code[i] != '\n' {
				i++
			}
			push(code[s:i], typOf(codeTypeComment))
		case c == '"' || c == '\'' || c == '`':
			s := i
			q := c
			i++
			for i < len(code) {
				if code[i] == '\\' && i+1 < len(code) {
					i += 2
				} else if code[i] == q {
					i++
					break
				} else {
					i++
				}
			}
			push(code[s:i], typOf(codeTypeString))
		case c >= '0' && c <= '9':
			s := i
			for i < len(code) && ((code[i] >= '0' && code[i] <= '9') || code[i] == '.' || code[i] == '_') {
				i++
			}
			push(code[s:i], typOf(codeTypeNumber))
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$':
			s := i
			for i < len(code) {
				ch := code[i]
				if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
					i++
				} else {
					break
				}
			}
			word := code[s:i]
			typ := codeTypeDefault
			if keywords[word] {
				typ = codeTypeKeyword
			} else {
				j := i
				for j < len(code) && (code[j] == ' ' || code[j] == '\t') {
					j++
				}
				if j < len(code) && code[j] == '(' {
					typ = codeTypeMethod
				}
			}
			push(word, typOf(typ))
		default:
			push(string(c), typOf(codeTypeDefault))
			i++
		}
	}
	return tokens
}

func unifiedCodeBlocks(blocks []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock) []any {
	typeMap := map[waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeHighlightType]string{
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_DEFAULT: "DEFAULT",
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_KEYWORD: "KEYWORD",
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_METHOD:  "METHOD",
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_STRING:  "STR",
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_NUMBER:  "NUMBER",
		waAICommonDeprecated.AIRichResponseCodeMetadata_AI_RICH_RESPONSE_CODE_HIGHLIGHT_COMMENT: "COMMENT",
	}
	var out []any
	for _, b := range blocks {
		out = append(out, map[string]any{
			"content": b.GetCodeContent(),
			"type":    typeMap[b.GetHighlightType()],
		})
	}
	return out
}
