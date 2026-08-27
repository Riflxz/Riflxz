package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// SearchResult is the small common result shape used by text-based search tools.
type SearchResult struct {
	Title       string
	Description string
	URL         string
	ImageURL    string
}

func requireSearchQuery(ctx context.Context, evt *events.Message, args, example string) (string, bool) {
	query := strings.TrimSpace(args)
	if query != "" {
		return query, true
	}
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("❌ Contoh: `%s%s`", Prefix, example))
	return "", false
}

func sendSearchImage(ctx context.Context, evt *events.Message, imageURL, caption string) error {
	// Fix: dlGetSafe — URL gambar biasanya hasil parse (Bing, dsb), bukan
	// URL fixed; dlGet biasa ikut request internal (SSRF).
	data, err := dlGetSafe(imageURL)
	if err != nil {
		return err
	}
	return sendImage(ctx, evt.Info.Chat, data, caption)
}

// ─── Wikipedia ───────────────────────────────────────────────────────────────

func handleWikipedia(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "wiki Indonesia")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")

	endpoint := "https://id.wikipedia.org/w/api.php?action=query&generator=search&gsrsearch=" +
		url.QueryEscape(query) + "&gsrlimit=1&prop=extracts|info|pageimages&exintro=1&explaintext=1&inprop=url&piprop=thumbnail&pithumbsize=600&format=json&origin=*"
	body, err := dlGet(endpoint, map[string]string{"Accept": "application/json"})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari Wikipedia: "+err.Error())
		return
	}
	var response struct {
		Query struct {
			Pages map[string]struct {
				Title     string `json:"title"`
				Extract   string `json:"extract"`
				FullURL   string `json:"fullurl"`
				Thumbnail struct {
					Source string `json:"source"`
				} `json:"thumbnail"`
			} `json:"pages"`
		} `json:"query"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Query.Pages) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Artikel tidak ditemukan.")
		return
	}
	for _, page := range response.Query.Pages {
		extract := strings.TrimSpace(page.Extract)
		if len(extract) > 2500 {
			extract = extract[:2500] + "…"
		}
		// Dua varian: plain untuk caption media (markdown tidak dirender di
		// caption), rich untuk fallback sendText kalau gambar gagal.
		plain := fmt.Sprintf("📚 WIKIPEDIA\n\n📌 %s\n\n%s\n\n🔗 %s", page.Title, extract, page.FullURL)
		rich := fmt.Sprintf("📚 *WIKIPEDIA*\n\n📌 *%s*\n\n%s\n\n🔗 %s", page.Title, extract, page.FullURL)
		if page.Thumbnail.Source != "" && sendSearchImage(ctx, evt, page.Thumbnail.Source, plain) == nil {
			break
		}
		sendText(ctx, evt.Info.Chat, rich)
		break
	}
	reactMsg(ctx, evt, "✅")
}

// ─── KBBI ────────────────────────────────────────────────────────────────────
// Scrape kbbi.web.id (mirror KBBI daring) — API faa (api-faa.my.id/faa/kbbi)
// sudah mati (404). Struktur: <div id="d1">..<div id="dN"> per definisi.

var reKBBIEntry = regexp.MustCompile(`(?s)<div id="d\d+"[^>]*>(.*?)</div>`)

func handleKBBI(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "kbbi narasi")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	body, err := dlGetSafe("https://kbbi.web.id/" + url.PathEscape(query))
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mengambil data KBBI: "+err.Error())
		return
	}
	doc := string(body)
	entries := reKBBIEntry.FindAllStringSubmatch(doc, -1)
	if len(entries) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Kata tidak ditemukan di KBBI.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📖 *KBBI — %s*\n\n", query)
	for i, m := range entries {
		if i >= 5 {
			break
		}
		// Bersihkan tag HTML + decode entity (&#183; → ·, dsb).
		text := reHTMLTag.ReplaceAllString(m[1], "")
		text = html.UnescapeString(text)
		text = strings.Join(strings.Fields(text), " ")
		fmt.Fprintf(&b, "%d. %s\n\n", i+1, text)
	}
	sendText(ctx, evt.Info.Chat, strings.TrimSpace(b.String()))
	reactMsg(ctx, evt, "✅")
}

// ─── Lyrics ──────────────────────────────────────────────────────────────────

func handleLyrics(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "lyrics another love")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	body, err := dlGet("https://lrclib.net/api/search?q="+url.QueryEscape(query), map[string]string{"Referer": "https://lrclib.net/"})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari lirik: "+err.Error())
		return
	}
	var songs []struct {
		TrackName   string `json:"trackName"`
		ArtistName  string `json:"artistName"`
		AlbumName   string `json:"albumName"`
		PlainLyrics string `json:"plainLyrics"`
	}
	if json.Unmarshal(body, &songs) != nil || len(songs) == 0 || songs[0].PlainLyrics == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Lirik tidak ditemukan.")
		return
	}
	lyrics := songs[0].PlainLyrics
	if len(lyrics) > 3200 {
		lyrics = lyrics[:3200] + "\n…(dipotong)"
	}
	sendText(ctx, evt.Info.Chat, fmt.Sprintf("🎵 *LYRICS*\n\n*%s*\n👤 %s\n💿 %s\n\n%s", songs[0].TrackName, songs[0].ArtistName, songs[0].AlbumName, lyrics))
	reactMsg(ctx, evt, "✅")
}

// ─── Apple Music (iTunes public API) ─────────────────────────────────────────

func handleAppleMusic(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "applemusic another love")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	body, err := dlGet("https://itunes.apple.com/search?media=music&entity=song&limit=5&term="+url.QueryEscape(query), nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari Apple Music: "+err.Error())
		return
	}
	var response struct {
		Results []struct {
			TrackName      string `json:"trackName"`
			ArtistName     string `json:"artistName"`
			CollectionName string `json:"collectionName"`
			TrackViewURL   string `json:"trackViewUrl"`
			ArtworkURL100  string `json:"artworkUrl100"`
			TrackTime      int64  `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Results) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Lagu tidak ditemukan.")
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "🍎 *APPLE MUSIC SEARCH*\nQuery: *%s*\n", query)
	for i, track := range response.Results {
		dur := time.Duration(track.TrackTime) * time.Millisecond
		fmt.Fprintf(&out, "\n%d. *%s*\n   👤 %s\n   💿 %s\n   ⏱ %s\n   🔗 %s\n", i+1, track.TrackName, track.ArtistName, track.CollectionName, dur.Truncate(time.Second), track.TrackViewURL)
	}
	caption := out.String()
	imageURL := strings.Replace(response.Results[0].ArtworkURL100, "100x100bb", "600x600bb", 1)
	if imageURL != "" && sendSearchImage(ctx, evt, imageURL, caption) == nil {
		reactMsg(ctx, evt, "✅")
		return
	}
	sendText(ctx, evt.Info.Chat, caption)
	reactMsg(ctx, evt, "✅")
}

// ─── YouTube search ───────────────────────────────────────────────────────────
// API: api.siputzx.my.id /api/s/youtube (pengganti scrape HTML youtube.com
// yang sering kena 403 bot-check). Fallback: scrape HTML lama.

var youtubeInitialDataRE = regexp.MustCompile(`(?s)var ytInitialData = (\{.*?\});</script>`)
var youtubeVideoRE = regexp.MustCompile(`"videoRenderer":\{"videoId":"([^"]+)".*?"title":\{"runs":\[\{"text":"([^"]+)"`) // first results only

// ytSearchResult — satu hasil pencarian YouTube.
type ytSearchResult struct {
	id    string
	title string
	thumb string
}

// siputzxYouTubeResp — response https://api.siputzx.my.id/api/s/youtube?query=...
type siputzxYouTubeResp struct {
	Status bool `json:"status"`
	Data   []struct {
		Type    string `json:"type"`
		VideoID string `json:"videoId"`
		Title   string `json:"title"`
		Image   string `json:"image"`
	} `json:"data"`
}

func handleYTSearch(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "yts another love")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")

	results, err := youtubeSearchSiputzx(query)
	if err != nil {
		// Fallback ke scrape HTML (tetap disimpan untuk jaga-jaga).
		results, err = youtubeSearchHTML(query)
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari YouTube: "+err.Error())
		return
	}
	if len(results) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Video tidak ditemukan.")
		return
	}

	var out strings.Builder
	fmt.Fprintf(&out, "▶️ *YOUTUBE SEARCH*\nQuery: *%s*\n", query)
	for i, r := range results {
		fmt.Fprintf(&out, "\n%d. *%s*\n   🔗 https://youtu.be/%s\n", i+1, r.title, r.id)
	}
	caption := out.String()
	thumb := results[0].thumb
	if thumb == "" {
		thumb = "https://i.ytimg.com/vi/" + results[0].id + "/hqdefault.jpg"
	}
	if sendSearchImage(ctx, evt, thumb, caption) != nil {
		sendText(ctx, evt.Info.Chat, caption)
	}
	reactMsg(ctx, evt, "✅")
}

// youtubeSearchSiputzx cari via api.siputzx.my.id (sumber utama).
func youtubeSearchSiputzx(query string) ([]ytSearchResult, error) {
	body, err := dlGetSafe("https://api.siputzx.my.id/api/s/youtube?query=" + url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	var res siputzxYouTubeResp
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	if !res.Status {
		return nil, fmt.Errorf("sedang tidak tersedia")
	}
	var out []ytSearchResult
	for _, d := range res.Data {
		if d.Type != "" && d.Type != "video" {
			continue
		}
		if d.VideoID == "" {
			continue
		}
		out = append(out, ytSearchResult{id: d.VideoID, title: d.Title, thumb: d.Image})
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

// youtubeSearchHTML fallback: scrape HTML youtube.com (regex lama).
func youtubeSearchHTML(query string) ([]ytSearchResult, error) {
	body, err := dlGet("https://www.youtube.com/results?search_query="+url.QueryEscape(query), map[string]string{"Accept-Language": "id-ID,id;q=0.9,en;q=0.8"})
	if err != nil {
		return nil, err
	}
	jsonBody := string(body)
	if match := youtubeInitialDataRE.FindStringSubmatch(jsonBody); len(match) > 1 {
		jsonBody = match[1]
	}
	matches := youtubeVideoRE.FindAllStringSubmatch(jsonBody, 10)
	var out []ytSearchResult
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 3 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		title := html.UnescapeString(strings.ReplaceAll(match[2], `\u0026`, "&"))
		out = append(out, ytSearchResult{id: match[1], title: title})
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

// ─── Bing Images ─────────────────────────────────────────────────────────────

var bingMAttrRE = regexp.MustCompile(`m=["']([^"']+)["']`)

func handleBingImage(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "bingimage anime landscape")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")
	body, err := dlGet("https://www.bing.com/images/search?q="+url.QueryEscape(query)+"&FORM=HDRSC2", nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gagal mencari gambar: "+err.Error())
		return
	}
	matches := bingMAttrRE.FindAllStringSubmatch(string(body), -1)
	sent := 0
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		var metadata struct {
			MURL string `json:"murl"`
			TURL string `json:"turl"`
		}
		if json.Unmarshal([]byte(html.UnescapeString(match[1])), &metadata) != nil {
			continue
		}
		imageURL := metadata.TURL
		if imageURL == "" {
			imageURL = metadata.MURL
		}
		if imageURL == "" || seen[imageURL] {
			continue
		}
		seen[imageURL] = true
		if sendSearchImage(ctx, evt, imageURL, fmt.Sprintf("🖼️ Bing Image — *%s* (%d/5)", query, sent+1)) == nil {
			sent++
		}
		if sent == 5 {
			break
		}
	}
	if sent == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Gambar tidak ditemukan.")
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Pinterest — cari → carousel pilih ──────────────────────────────────────
// API: nembak LANGSUNG ke web Pinterest (resource/BaseSearchResource/get) —
// bukan API pihak ketiga. Fallback siputzx hanya kalau Pinterest menolak.
//
// Pola 2 level:
//   Level 1 → `!pinterest <query>` → carousel preview 5-10 hasil (swipe),
//            tiap kartu ada tombol "Kirim" → `!pinpick <n>`
//   Level 2 → `!pinpick <n>` → kirim gambar + link pin terpilih (session 15 mnt)

// pinResult — satu hasil pencarian Pinterest.
type pinResult struct {
	Title    string
	ImageURL string
	Link     string
}

// pinterestSearchDirect — scrape langsung endpoint internal web Pinterest
// (BaseSearchResource/get). Butuh payload options LENGKAP + header khusus
// (X-Requested-With, x-pinterest-*) — tanpa itu Pinterest balas
// "Invalid Resource Request". Guest (tanpa login) tetap jalan.
func pinterestSearchDirect(query string) ([]pinResult, error) {
	options := map[string]any{
		"query":                    query,
		"scope":                    "pins",
		"appliedProductFilters":    "---",
		"domains":                  nil,
		"user":                     nil,
		"seoDrawerEnabled":         false,
		"applied_unified_filters":  nil,
		"auto_correction_disabled": false,
		"filter_genai":             false,
		"journey_depth":            nil,
		"source_id":                nil,
		"source_module_id":         nil,
		"source_url":               "/search/pins/?q=" + url.QueryEscape(query),
		"static_feed":              false,
		"selected_one_bar_modules": nil,
		"query_pin_sigs":           nil,
		"page_size":                25,
		"gated":                    true,
		"price_max":                nil,
		"price_min":                nil,
		"query_image_pins":         nil,
		"request_params":           nil,
		"top_pin_ids":              nil,
		"article":                  nil,
		"corpus":                   nil,
		"filters":                  nil,
		"rs":                       "direct_navigation",
	}
	payload, err := json.Marshal(map[string]any{"options": options, "context": map[string]any{}})
	if err != nil {
		return nil, err
	}
	apiURL := "https://www.pinterest.com/resource/BaseSearchResource/get/?" +
		"source_url=" + url.QueryEscape("/search/pins/?q="+url.QueryEscape(query)) +
		"&data=" + url.QueryEscape(string(payload)) +
		"&_=" + fmt.Sprintf("%d", time.Now().UnixMilli())

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*, q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("x-pinterest-source-url", "/search/pins/?q="+url.QueryEscape(query))
	req.Header.Set("x-pinterest-appstate", "active")
	req.Header.Set("Referer", "https://www.pinterest.com/")
	req.Header.Set("x-pinterest-pws-handler", "www/search/[scope].js")
	req.Header.Set("x-app-version", "be501f2")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")

	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		ResourceResponse struct {
			Data struct {
				Results []struct {
					ID     string `json:"id"`
					Images struct {
						Orig struct {
							URL string `json:"url"`
						} `json:"orig"`
					} `json:"images"`
				} `json:"results"`
			} `json:"data"`
		} `json:"resource_response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var results []pinResult
	for _, r := range parsed.ResourceResponse.Data.Results {
		if r.ID == "" || r.Images.Orig.URL == "" {
			continue
		}
		results = append(results, pinResult{
			Title:    query,
			ImageURL: r.Images.Orig.URL,
			Link:     "https://pinterest.com/pin/" + r.ID + "/",
		})
		if len(results) == 10 {
			break
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("pinterest: tidak ada hasil")
	}
	return results, nil
}

// pinSession — hasil pencarian per chat + timestamp (TTL).
type pinSession struct {
	results []pinResult
	ts      time.Time
}

// pinSessions — sesi hasil per chat untuk command "pinpick <idx>".
// TTL 15 menit; cleanup lazy saat akses (cukup untuk bot).
var pinSessions = struct {
	sync.Mutex
	m map[string]*pinSession
}{m: make(map[string]*pinSession)}

const pinSessionTTL = 15 * time.Minute

func handlePinterest(ctx context.Context, evt *events.Message, args string) {
	query, ok := requireSearchQuery(ctx, evt, args, "pin anime girl")
	if !ok {
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Nembak langsung ke web Pinterest; kalau ditolak, fallback siputzx.
	results, err := pinterestSearchDirect(query)
	if err != nil {
		results = nil
		body, ferr := dlGetSafe("https://api.siputzx.my.id/api/s/pinterest?query=" + url.QueryEscape(query))
		if ferr != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, evt.Info.Chat, "❌ Pinterest sedang tidak tersedia.")
			return
		}
		var response struct {
			Status bool `json:"status"`
			Data   []struct {
				ID        string `json:"id"`
				GridTitle string `json:"grid_title"`
				ImageURL  string `json:"image_url"`
				Pin       string `json:"pin"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &response) != nil || !response.Status || len(response.Data) == 0 {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, evt.Info.Chat, "❌ Tidak ada hasil untuk: "+query)
			return
		}
		for _, r := range response.Data {
			if r.ImageURL == "" {
				continue
			}
			link := r.Pin
			if link == "" {
				link = "https://pinterest.com/pin/" + r.ID
			}
			results = append(results, pinResult{Title: r.GridTitle, ImageURL: r.ImageURL, Link: link})
			if len(results) == 10 {
				break
			}
		}
	}
	if len(results) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, evt.Info.Chat, "❌ Pinterest image tidak ditemukan.")
		return
	}

	chatKey := evt.Info.Chat.String()
	pinSessions.Lock()
	pinSessions.m[chatKey] = &pinSession{results: results, ts: time.Now()}
	pinSessions.Unlock()

	// Tampilkan hasil sebagai carousel (swipe horizontal) — tiap kartu ada
	// tombol "Kirim" → `!pinpick <n>`. Session tetap disimpan supaya
	// `!pinpick <n>` bisa dipakai kirim ulang hasil apa pun.
	sendPinCarousel(ctx, evt.Info.Chat, evt, query, results)
	reactMsg(ctx, evt, "✅")
}

// lookupPinSession — ambil session per chat dengan cek TTL (cleanup lazy).
func lookupPinSession(chatKey string) *pinSession {
	pinSessions.Lock()
	defer pinSessions.Unlock()
	s := pinSessions.m[chatKey]
	if s != nil && time.Since(s.ts) > pinSessionTTL {
		delete(pinSessions.m, chatKey)
		return nil
	}
	return s
}

// pinPickerRows — bangun row dropdown dari hasil pencarian (murni, mudah dites).
// Row id `pinpick <idx>` dipakai router — format ini jangan diubah.
func pinPickerRows(query, p string, results []pinResult) []listRow {
	rows := make([]listRow, 0, len(results))
	for i, r := range results {
		title := r.Title
		if title == "" {
			title = query
		}
		if len(title) > 48 {
			title = title[:48] + "…"
		}
		rows = append(rows, listRow{
			title: fmt.Sprintf("%d. %s", i+1, title),
			desc:  r.Link,
			id:    fmt.Sprintf("%spinpick %d", p, i+1),
		})
	}
	return rows
}

// sendPinPicker — dropdown single-select daftar hasil; row id "pinpick <idx>".
// Fallback plain text bernomor kalau client tidak mendukung pesan interaktif.
func sendPinPicker(ctx context.Context, chat types.JID, evt *events.Message, query string, results []pinResult) {
	p := Prefix
	rows := pinPickerRows(query, p, results)

	b := NewMsgBuilder().
		SetHeader(BotName, "Pinterest · "+query).
		SetBody(fmt.Sprintf("*%d hasil* untuk *%s* — pilih salah satu:", len(results), query)).
		SetFooter(channelFooter()).
		SetContextInfo(newsletterCtxInfo(ctx)).
		AddSelectButton("Pilih Gambar", []listSection{{title: "Hasil Pinterest", rows: rows}})

	if err := b.Send(ctx, chat); err != nil {
		pool.logger.Warn().Err(err).Msg("pin picker gagal — fallback plain text")
		var sb strings.Builder
		fmt.Fprintf(&sb, "📌 *PINTEREST* — %s\n", query)
		for i, r := range results {
			title := r.Title
			if title == "" {
				title = query
			}
			fmt.Fprintf(&sb, "\n%d. *%s*\n%s", i+1, title, r.Link)
		}
		fmt.Fprintf(&sb, "\n\n_Balas `%spinpick <nomor>` untuk kirim gambarnya_", p)
		sendText(ctx, chat, sb.String())
	}
}

// sendPinCarousel — preview semua hasil sebagai carousel (swipe horizontal).
// Tiap kartu: 1 gambar + tombol "Kirim" → "pinpick <idx>". Batas kartu
// carousel = 10, sama dengan jumlah maks hasil. Client yang tidak mendukung
// carousel → fallback ke sendPinPicker (dropdown) → teks bernomor.
func sendPinCarousel(ctx context.Context, chat types.JID, evt *events.Message, query string, results []pinResult) {
	cb := NewCarouselBuilder()
	for i, r := range results {
		data, err := dlGetSafeLimit(r.ImageURL, 5<<20)
		if err != nil {
			pool.logger.Warn().Err(err).Str("url", r.ImageURL).Msg("pin carousel: download preview gagal")
			continue
		}
		up, err := waClient.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			pool.logger.Warn().Err(err).Msg("pin carousel: upload preview gagal")
			continue
		}
		mime := sniffImageMimetype(data)
		if mime == "" {
			mime = "image/jpeg"
		}
		title := r.Title
		if title == "" {
			title = query
		}
		if len(title) > 48 {
			title = title[:48] + "…"
		}
		card := NewMsgBuilder().
			SetHeader(fmt.Sprintf("Pinterest · %d/%d", i+1, len(results)), title).
			SetImageHeader(&menuImgCache{up: &up, mimetype: mime}).
			SetBody(title).
			AddQRButton("Kirim", fmt.Sprintf("%spinpick %d", Prefix, i+1))
		if err := cb.AddCard(card); err != nil {
			pool.logger.Warn().Err(err).Msg("pin carousel: kartu ditolak")
			continue
		}
	}
	if cb.Len() == 0 {
		sendPinPicker(ctx, chat, evt, query, results)
		return
	}
	if sendErr := cb.Send(ctx, chat); sendErr == nil {
		return
	} else {
		pool.logger.Warn().Err(sendErr).Msg("pin carousel: kirim gagal, fallback dropdown")
	}
	sendPinPicker(ctx, chat, evt, query, results)
}

// handlePinPick — Level 2 navigasi: "pinpick <idx>" → kirim hasil yang dipilih.
func handlePinPick(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	idx, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil || idx < 1 {
		sendText(ctx, chat, "❌ Contoh: `"+Prefix+"pinpick 1` — nomor dari daftar `"+Prefix+"pinterest`")
		return
	}

	sess := lookupPinSession(chat.String())
	if sess == nil || idx > len(sess.results) {
		sendText(ctx, chat, "❌ Daftar hasil habis/kedaluwarsa — cari ulang dengan `"+Prefix+"pinterest <query>`")
		return
	}
	r := sess.results[idx-1]
	title := r.Title
	if title == "" {
		title = "Pinterest"
	}

	reactMsg(ctx, evt, "⏳")
	if err := sendSearchImage(ctx, evt, r.ImageURL, fmt.Sprintf("📌 *%s*\n%s", title, r.Link)); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil gambar: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}
