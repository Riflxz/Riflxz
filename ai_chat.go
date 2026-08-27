package main

// ai_chat.go — Fitur AI chat via feelbetterbot.com.
//
// !ai on/off — khusus owner. Setelah on, pesan non-command yang diawali
// "yuuki"/"yuki" (mis. "yuuki apa kabar") diteruskan ke AI — berlaku untuk
// SEMUA user. Session percakapan per user disimpan otomatis (persist ke
// database/ai_state.json), bisa dilihat via !ai list dan dilanjutkan via
// !ai load <nomor>.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Konfigurasi ─────────────────────────────────────────────────────────────

const (
	aiAPIURL       = "https://feelbetterbot.com/"
	aiUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	aiMaxHistory   = 10  // jumlah pesan terakhir yang dikirim ke API
	aiMaxPromptLen = 3000
	aiMaxReplyLen  = 4000 // batas aman panjang pesan WA
	aiHTTPTimeout  = 90 * time.Second
)

// aiDefaultPrompt — persona Yuuki: profesional tapi tetap ramah & imut,
// sesuai nama bot. Bukan alay (bukan "uwu/hihi/aciikk").
const aiDefaultPrompt = "Kamu adalah Yuuki, asisten pribadi virtual yang ramah, ceria, dan imut. " +
	"Panggil pengguna dengan sapaan hangat seperti 'Kak'. Bicaralah dalam bahasa Indonesia yang santai " +
	"namun sopan dan profesional. Kamu membantu menjawab pertanyaan, menemani ngobrol, dan memberikan " +
	"informasi yang akurat dan bermanfaat. Gunakan emoji secukupnya untuk kesan ramah, jangan berlebihan."

// ─── State (persist) ─────────────────────────────────────────────────────────

type aiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiSession struct {
	SystemPrompt string      `json:"systemPrompt"`
	History      []aiMessage `json:"history"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

type aiStateFile struct {
	Enabled  bool                 `json:"enabled"`
	Sessions map[string]aiSession `json:"sessions"` // key = nomor user
}

const aiStatePath = "database/ai_state.json"

var (
	aiStateOnce sync.Once
	aiStateMu   sync.Mutex // serialisasi state + I/O file
	aiEnabled   bool
	aiSessions  = map[string]aiSession{}
)

// ensureAIState load state dari file sekali saat pertama dipakai.
func ensureAIState() {
	aiStateOnce.Do(func() {
		data, err := os.ReadFile(aiStatePath)
		if err != nil {
			return // belum ada file — mulai kosong
		}
		var st aiStateFile
		if err := json.Unmarshal(data, &st); err != nil {
			pool.logger.Warn().Err(err).Str("path", aiStatePath).Msg("ai_state: JSON korup, mulai kosong")
			return
		}
		aiEnabled = st.Enabled
		if st.Sessions != nil {
			aiSessions = st.Sessions
		}
	})
}

// saveAIState tulis snapshot state ke file. Dipanggil setelah mutasi;
// kegagalan hanya di-log, tidak menggagalkan operasi.
func saveAIState() {
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	if err := os.MkdirAll("database", 0o755); err != nil {
		pool.logger.Warn().Err(err).Msg("ai_state: gagal buat folder database")
		return
	}
	data, err := json.MarshalIndent(aiStateFile{Enabled: aiEnabled, Sessions: aiSessions}, "", "  ")
	if err != nil {
		pool.logger.Warn().Err(err).Msg("ai_state: gagal encode")
		return
	}
	if err := os.WriteFile(aiStatePath, data, 0o644); err != nil {
		pool.logger.Warn().Err(err).Msg("ai_state: gagal simpan")
	}
}

func aiChatEnabled() bool {
	ensureAIState()
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	return aiEnabled
}

func setAIEnabled(b bool) {
	ensureAIState()
	aiStateMu.Lock()
	aiEnabled = b
	aiStateMu.Unlock()
	saveAIState()
}

// aiGetSession ambil salinan session milik user (kosong kalau belum ada).
func aiGetSession(user string) aiSession {
	ensureAIState()
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	return aiSessions[user]
}

// aiSaveSession simpan session user (history + prompt + timestamp).
func aiSaveSession(user string, s aiSession) {
	ensureAIState()
	aiStateMu.Lock()
	s.UpdatedAt = time.Now()
	aiSessions[user] = s
	aiStateMu.Unlock()
	saveAIState()
}

// aiResetSession hapus session user (mulai percakapan baru).
func aiResetSession(user string) {
	ensureAIState()
	aiStateMu.Lock()
	delete(aiSessions, user)
	aiStateMu.Unlock()
	saveAIState()
}

// aiSessionInfo — ringkasan satu session buat !ai list.
type aiSessionInfo struct {
	User      string
	Count     int
	UpdatedAt time.Time
}

// aiListSessions daftar semua session, urut dari yang terbaru.
func aiListSessions() []aiSessionInfo {
	ensureAIState()
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	out := make([]aiSessionInfo, 0, len(aiSessions))
	for user, s := range aiSessions {
		out = append(out, aiSessionInfo{User: user, Count: len(s.History), UpdatedAt: s.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// ─── API feelbetterbot.com ───────────────────────────────────────────────────

// callFeelbetter kirim riwayat pesan ke API, parse stream SSE, balikin teks
// lengkap. Mirip parseStream di source JS: baca semua baris, ambil yang
// diawali "data:", gabungkan delta-delta JSON.
func callFeelbetter(messages []aiMessage) (string, error) {
	body, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", aiAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", aiUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://feelbetterbot.com")
	req.Header.Set("Referer", "https://feelbetterbot.com/")

	client := &http.Client{Timeout: aiHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	text := aiParseStream(raw)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("respon kosong dari server")
	}
	return text, nil
}

// aiParseStream — port parseStream dari feelbetterEngine.js.
func aiParseStream(raw []byte) string {
	buffer := string(raw)
	lines := strings.Split(buffer, "\n")

	var fullText strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var parsed struct {
			Type    string `json:"type"`
			Delta   string `json:"delta"`
			Content string `json:"content"`
			Text    string `json:"text"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Text string `json:"text"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			fullText.WriteString(data)
			continue
		}
		switch {
		case parsed.Type == "text-delta":
			fullText.WriteString(parsed.Delta)
		case len(parsed.Choices) > 0:
			if parsed.Choices[0].Delta.Content != "" {
				fullText.WriteString(parsed.Choices[0].Delta.Content)
			} else {
				fullText.WriteString(parsed.Choices[0].Text)
			}
		case parsed.Content != "":
			fullText.WriteString(parsed.Content)
		case parsed.Text != "":
			fullText.WriteString(parsed.Text)
		case parsed.Delta != "":
			fullText.WriteString(parsed.Delta)
		}
	}

	out := strings.TrimSpace(fullText.String())
	if out != "" {
		return out
	}
	// Fallback: gabungkan semua baris non-kosong non-event (pola JS).
	var sb strings.Builder
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "event:") || strings.HasPrefix(t, "id:") || strings.HasPrefix(t, "retry:") {
			continue
		}
		sb.WriteString(t)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// ─── Chat (semua user) ───────────────────────────────────────────────────────

// aiExtractPrompt cek apakah teks diawali "yuuki"/"yuki" (case-insensitive).
// Return prompt tanpa nama panggilan; ok=false kalau bukan panggilan.
func aiExtractPrompt(text string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, name := range []string{"yuuki", "yuki"} {
		if lower == name {
			return "", true // cuma nama — sapa saja
		}
		if strings.HasPrefix(lower, name+" ") || strings.HasPrefix(lower, name+",") ||
			strings.HasPrefix(lower, name+".") || strings.HasPrefix(lower, name+"!") ||
			strings.HasPrefix(lower, name+"?") {
			rest := strings.TrimSpace(text[len(name):])
			rest = strings.TrimLeft(rest, ",.!? ")
			return rest, true
		}
	}
	return "", false
}

// handleAiChat — dipanggil dari router untuk pesan "yuuki/yuki ...".
// Semua user boleh; session otomatis per user.
func handleAiChat(ctx context.Context, evt *events.Message, prompt string) {
	chat := evt.Info.Chat
	user := senderUser(evt)

	if strings.TrimSpace(prompt) == "" {
		sendText(ctx, chat, "Halo Kak! ✨ Yuuki di sini. Ada yang bisa Yuuki bantu?")
		return
	}

	reactMsg(ctx, evt, "💭")
	reply, err := aiChat(user, prompt)
	if err != nil {
		pool.logger.Warn().Err(err).Str("user", user).Msg("ai_chat: gagal panggil API")
		sendText(ctx, chat, "😵 Maaf Kak, Yuuki lagi gangguan. Coba lagi sebentar ya 🙏")
		return
	}
	if len(reply) > aiMaxReplyLen {
		reply = reply[:aiMaxReplyLen] + "…"
	}
	sendText(ctx, chat, reply)
}

// aiChat — satu putaran percakapan: ambil session user, kirim ke API,
// simpan balasan ke history. History dipotong ke aiMaxHistory pesan terakhir.
func aiChat(user, message string) (string, error) {
	sess := aiGetSession(user)
	if sess.SystemPrompt == "" {
		sess.SystemPrompt = aiDefaultPrompt
	}
	if len(sess.SystemPrompt) > aiMaxPromptLen {
		sess.SystemPrompt = sess.SystemPrompt[:aiMaxPromptLen]
	}

	hist := sess.History
	if len(hist) > aiMaxHistory {
		hist = hist[len(hist)-aiMaxHistory:]
	}
	hist = append(hist, aiMessage{Role: "user", Content: message})

	msgs := make([]aiMessage, 0, len(hist)+1)
	msgs = append(msgs, aiMessage{Role: "assistant", Content: sess.SystemPrompt})
	msgs = append(msgs, hist...)

	reply, err := callFeelbetter(msgs)
	if err != nil {
		return "", err
	}

	sess.History = append(sess.History,
		aiMessage{Role: "user", Content: message},
		aiMessage{Role: "assistant", Content: reply},
	)
	aiSaveSession(user, sess)
	return reply, nil
}

// ─── Command !ai (khusus owner) ──────────────────────────────────────────────

// handleAiCmd — !ai on/off/list/load/new. Owner-only (guard di router).
func handleAiCmd(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) == 0 {
		st := "mati"
		if aiChatEnabled() {
			st = "aktif"
		}
		sendText(ctx, chat, fmt.Sprintf(
			"🤖 *AI Chat — Yuuki*\n\n"+
				"> Status: *%s*\n\n"+
				"*Format:*\n"+
				"> `%sai on` — nyalakan AI chat\n"+
				"> `%sai off` — matikan\n"+
				"> `%sai list` — daftar session percakapan\n"+
				"> `%sai load 628xxx` — lanjutkan session user itu\n"+
				"> `%sai new` — mulai percakapan baru (session kamu)\n\n"+
				"*Cara pakai (semua user):*\n"+
				"> Ketik `yuuki <pertanyaan>` atau `yuki <pertanyaan>` di chat\n"+
				"> Contoh: `yuuki apa kabar?`",
			st, Prefix, Prefix, Prefix, Prefix, Prefix))
		return
	}

	switch strings.ToLower(fields[0]) {
	case "on", "nyalakan", "enable":
		setAIEnabled(true)
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "🤖 *AI Chat aktif!*\n\n"+
			"> Semua user bisa ngobrol dengan Yuuki:\n"+
			"> `yuuki <pertanyaan>` atau `yuki <pertanyaan>`")

	case "off", "matikan", "disable":
		setAIEnabled(false)
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "🤖 *AI Chat dimatikan.*")

	case "list", "daftar":
		sessions := aiListSessions()
		if len(sessions) == 0 {
			sendText(ctx, chat, "📭 Belum ada session percakapan.")
			return
		}
		var b strings.Builder
		b.WriteString("📋 *Session AI Chat*\n\n")
		for i, s := range sessions {
			fmt.Fprintf(&b, "%d. `%s` — %d pesan • %s\n",
				i+1, s.User, s.Count, s.UpdatedAt.Format("02 Jan 15:04"))
		}
		b.WriteString("\n> Lanjutkan: `" + Prefix + "ai load <nomor>`")
		sendText(ctx, chat, b.String())

	case "load", "muat":
		if len(fields) < 2 {
			sendText(ctx, chat, "❌ Format: `!ai load 628xxx` — nomor dari `!ai list`.")
			return
		}
		target := cleanJIDNumber(fields[1])
		sess := aiGetSession(target)
		if sess.SystemPrompt == "" && len(sess.History) == 0 {
			sendText(ctx, chat, "❌ Session `"+target+"` tidak ditemukan.")
			return
		}
		// Salin session target ke session pemanggil — percakapan dilanjutkan
		// dari posisi user itu.
		aiSaveSession(senderUser(evt), sess)
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, fmt.Sprintf(
			"📂 Session `%s` dimuat (%d pesan).\n"+
				"> Lanjutkan dengan `yuuki <pertanyaan>`.",
			target, len(sess.History)))

	case "new", "reset", "baru":
		aiResetSession(senderUser(evt))
		reactMsg(ctx, evt, "✅")
		sendText(ctx, chat, "🆕 Session percakapan kamu direset. Yuuki mulai dari awal lagi ✨")

	default:
		sendText(ctx, chat, "❌ Sub-command tidak dikenal. Ketik `!ai` untuk panduan.")
	}
}