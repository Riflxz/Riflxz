package main

// scanrepo.go — !scanrepo: scan repository GitHub via scanrepo.dev.
// API: POST https://www.scanrepo.dev/api/scan (streaming NDJSON).
// Bisa dipakai semua user (public mode).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

const scanrepoAPI = "https://www.scanrepo.dev/api/scan"

// scanrepoResult — data hasil scan dari scanrepo.dev.
type scanrepoResult struct {
	Meta struct {
		Provider string `json:"provider"`
		Owner    string `json:"owner"`
		Repo     string `json:"repo"`
		Language string `json:"language"`
		Stars    int    `json:"stars"`
		Forks    int    `json:"forks"`
	} `json:"meta"`
	RiskScore    float64 `json:"riskScore"`
	RiskLevel    string  `json:"riskLevel"`
	Cached       bool    `json:"cached"`
	FilesScanned int     `json:"filesScanned"`
	TotalFiles   int     `json:"totalRepoFiles"`
	Coverage     float64 `json:"coverage"`
	CommitSHA    string  `json:"commitSha"`
	ScannedAt    string  `json:"scannedAt"`
	ScanVersion  string  `json:"scannerVersion"`
	Badges       []struct {
		Label string `json:"label"`
	} `json:"badges"`
	Categories []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Score   float64 `json:"score"`
		Findings []struct {
			Title  string `json:"title"`
			File   string `json:"file"`
			Line   int    `json:"line"`
			Points int    `json:"points"`
		} `json:"findings"`
	} `json:"categories"`
}

// scanrepoNDJSON — wrapper satu baris NDJSON dari streaming response.
type scanrepoNDJSON struct {
	Type   string          `json:"type"`
	Data   *scanrepoResult `json:"data"`
	Error  string          `json:"error"`
	Step   string          `json:"step"`
}

// handleScanRepo — scan repository GitHub via scanrepo.dev.
// Cmd: !scanrepo <github-url>
func handleScanRepo(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	url := strings.TrimSpace(args)
	if url == "" {
		sendText(ctx, chat, fmt.Sprintf(
			"╔═ 『 🔍 SCANREPO 』\n"+
				"║ Scan repository GitHub untuk security risk.\n"+
				"╠══════════════════════════\n"+
				"║\n"+
				"║ *Format:*\n"+
				"║ > `%sscanrepo <github-url>`\n"+
				"║\n"+
				"║ *Contoh:*\n"+
				"║ > `%sscanrepo https://github.com/owner/repo`\n"+
				"╚══════════════════════════", Prefix, Prefix))
		return
	}

	// Normalisasi URL.
	if !strings.HasPrefix(strings.ToLower(url), "http") {
		url = "https://" + url
	}

	reactMsg(ctx, evt, "🔍")

	// POST ke API.
	reqBody, _ := json.Marshal(map[string]string{"url": url})
	req, err := http.NewRequest("POST", scanrepoAPI, bytes.NewReader(reqBody))
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal membuat request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0.0.0 Mobile Safari/537.36")

	// Timeout 120 detik — scan bisa lama.
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi ScanRepo: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Coba baca error JSON.
		var errResp struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		reactMsg(ctx, evt, "❌")
		msg := fmt.Sprintf("❌ ScanRepo error (HTTP %d)", resp.StatusCode)
		if errResp.Error != "" {
			msg += ": " + errResp.Error
		}
		sendText(ctx, chat, msg)
		return
	}

	// Baca streaming NDJSON — cari baris {type:"result"}.
	var result *scanrepoResult
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // max 1MB per line
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item scanrepoNDJSON
		if json.Unmarshal([]byte(line), &item) != nil {
			continue
		}
		switch item.Type {
		case "result":
			result = item.Data
		case "error":
			reactMsg(ctx, evt, "❌")
			errMsg := item.Error
			if errMsg == "" {
				errMsg = "scan error"
			}
			sendText(ctx, chat, "❌ ScanRepo: "+errMsg)
			return
		}
	}

	if result == nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Tidak ada hasil scan dari ScanRepo.")
		return
	}

	// Format output untuk WhatsApp.
	text := formatScanRepoResult(result, url)
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, text)
}

// riskEmoji — emoji berdasarkan risk level.
func riskEmoji(level string) string {
	switch strings.ToLower(level) {
	case "critical":
		return "🔴"
	case "high":
		return "🟠"
	case "medium":
		return "🟡"
	case "low":
		return "🟢"
	default:
		return "⚪"
	}
}

// formatScanRepoResult — format hasil scan jadi teks WhatsApp dengan box-drawing.
func formatScanRepoResult(r *scanrepoResult, url string) string {
	m := r.Meta
	repo := m.Owner + "/" + m.Repo
	if repo == "/" {
		repo = url
	}

	// Kumpulkan semua findings dari semua kategori (flatten).
	var allFindings []struct {
		Title  string
		File   string
		Line   int
		Points int
	}
	totalFindings := 0
	for _, cat := range r.Categories {
		totalFindings += len(cat.Findings)
		for _, f := range cat.Findings {
			allFindings = append(allFindings, struct {
				Title  string
				File   string
				Line   int
				Points int
			}{f.Title, f.File, f.Line, f.Points})
		}
	}

	var b strings.Builder

	// ════════════════════════════════════════════════════════════════════════════
	// HEADER
	// ════════════════════════════════════════════════════════════════════════════
	fmt.Fprintf(&b, "╔═ 『 🔍 SCANREPO 』\n")
	fmt.Fprintf(&b, "║ 📦 *%s*\n", repo)
	fmt.Fprintf(&b, "╠══════════════════════════\n")

	// ════════════════════════════════════════════════════════════════════════════
	// REPO INFO
	// ════════════════════════════════════════════════════════════════════════════
	if m.Language != "" || m.Stars > 0 || m.Forks > 0 {
		fmt.Fprintf(&b, "║\n")
		fmt.Fprintf(&b, "║ 📋 *Repo Info*\n")
		if m.Language != "" {
			fmt.Fprintf(&b, "║ ├ 💻 Language: *%s*\n", m.Language)
		}
		if m.Stars > 0 || m.Forks > 0 {
			fmt.Fprintf(&b, "║ ├ ⭐ Stars: *%s*", shortNum(m.Stars))
			if m.Forks > 0 {
				fmt.Fprintf(&b, "   🍴 Forks: *%s*", shortNum(m.Forks))
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "║ └ 📁 Files: *%d / %d*", r.FilesScanned, r.TotalFiles)
		if r.Coverage > 0 {
			fmt.Fprintf(&b, "  📊 *%.0f%%*", r.Coverage)
		}
		b.WriteString("\n")
	}

	// ════════════════════════════════════════════════════════════════════════════
	// RISK SCORE
	// ════════════════════════════════════════════════════════════════════════════
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	fmt.Fprintf(&b, "║\n")
	fmt.Fprintf(&b, "║ %s *Risk Score:* *%.0f / 100*\n", riskEmoji(r.RiskLevel), r.RiskScore)
	fmt.Fprintf(&b, "║ ├ Level: *%s*\n", strings.ToUpper(r.RiskLevel))
	fmt.Fprintf(&b, "║ ├ Commit: `%s`\n", shortSHA(r.CommitSHA))
	fmt.Fprintf(&b, "║ └ Cached: *%s*\n", boolID(r.Cached))

	// ════════════════════════════════════════════════════════════════════════════
	// BADGES
	// ════════════════════════════════════════════════════════════════════════════
	if len(r.Badges) > 0 {
		var labels []string
		for _, bg := range r.Badges {
			if bg.Label != "" {
				labels = append(labels, bg.Label)
			}
		}
		if len(labels) > 0 {
			fmt.Fprintf(&b, "╠══════════════════════════\n")
			fmt.Fprintf(&b, "║\n")
			fmt.Fprintf(&b, "║ 🏅 *Badges*\n")
			for _, l := range labels {
				fmt.Fprintf(&b, "║ └ %s\n", l)
			}
		}
	}

	// ════════════════════════════════════════════════════════════════════════════
	// CATEGORIES
	// ════════════════════════════════════════════════════════════════════════════
	if len(r.Categories) > 0 {
		fmt.Fprintf(&b, "╠══════════════════════════\n")
		fmt.Fprintf(&b, "║\n")
		fmt.Fprintf(&b, "║ 📋 *Categories*\n")
		for ci := 0; ci < len(r.Categories); ci++ {
			cat := r.Categories[ci]
			findingsCount := len(cat.Findings)
			connector := "├"
			if ci == len(r.Categories)-1 {
				connector = "└"
			}
			fmt.Fprintf(&b, "║ %s %s: *%.0f* (%d)\n", connector, cat.Name, cat.Score, findingsCount)
		}
	}

	// ════════════════════════════════════════════════════════════════════════════
	// TOP FINDINGS (dari flattened list)
	// ════════════════════════════════════════════════════════════════════════════
	if len(allFindings) > 0 {
		fmt.Fprintf(&b, "╠══════════════════════════\n")
		fmt.Fprintf(&b, "║\n")
		fmt.Fprintf(&b, "║ ⚠️ *Findings* (%d total)\n", totalFindings)
		maxShow := 8
		if len(allFindings) < maxShow {
			maxShow = len(allFindings)
		}
		for i := 0; i < maxShow; i++ {
			f := allFindings[i]
			file := f.File
			if file == "" {
				file = "?"
			}
			if f.Line > 0 {
				file = fmt.Sprintf("%s:%d", file, f.Line)
			}
			connector := "├"
			if i == maxShow-1 && maxShow == len(allFindings) {
				connector = "└"
			}
			fmt.Fprintf(&b, "║ %s *%s*\n", connector, f.Title)
			fmt.Fprintf(&b, "║   ├ 📁 `%s`\n", file)
			fmt.Fprintf(&b, "║   └ 📌 +%dpts\n", f.Points)
		}
		if len(allFindings) > maxShow {
			fmt.Fprintf(&b, "║ └ _…+ %d lainnya_\n", len(allFindings)-maxShow)
		}
	}

	// ════════════════════════════════════════════════════════════════════════════
	// FOOTER
	// ════════════════════════════════════════════════════════════════════════════
	fmt.Fprintf(&b, "╠══════════════════════════\n")
	fmt.Fprintf(&b, "║ 🔗 %s\n", scanrepoLink(m.Provider, m.Owner, m.Repo))
	fmt.Fprintf(&b, "╚══════════════════════════")

	return b.String()
}

// shortSHA — potong SHA jadi 7 char.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// boolID — bool → Ya/Tidak.
func boolID(v bool) string {
	if v {
		return "Ya"
	}
	return "Tidak"
}

// scanrepoLink — format link result page.
func scanrepoLink(provider, owner, repo string) string {
	if provider == "" || owner == "" || repo == "" {
		return "https://www.scanrepo.dev"
	}
	return fmt.Sprintf("https://www.scanrepo.dev/scan/%s/%s/%s", provider, owner, repo)
}
