package main

// features_fun_social.go — Fitur hiburan & sosial.
// Berisi: getpaste (Pastebin raw), cekganteng (random), cekbucin (random),
// robloxstalk (API Roblox publik), jodoh (random kecocokan).
//
// Prinsip:
// - API publik / tanpa API key berbayar.
// - Pakai helper yang sudah ada: dlGetSafe, sendText, reactMsg, sendImage.
// - Gaya teks SC sendiri.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// ─── Get Pastebin ─────────────────────────────────────────────────────────────
// Ambil konten raw Pastebin — port dari tools/getpaste.js.
// Output pakai MessageBuilder (AIRich) biar rapi.
// Cmd: !getpaste <link pastebin>, !pastebin, !getpb

var rePastebin = regexp.MustCompile(`(?i)pastebin\.com/(?:raw/)?([a-zA-Z0-9]+)`)

// langSignal — satu sinyal deteksi bahasa (regex + bobot).
// Semakin spesifik sinyalnya, semakin besar bobotnya.
type langSignal struct {
	lang   string
	re     *regexp.Regexp
	weight int
}

// langSignals — daftar sinyal untuk scoring deteksi bahasa.
// Dipindai di SELURUH konten (bukan cuma awal), jadi paste yang
// diawali komentar/header tetap terdeteksi benar.
var langSignals = []langSignal{
	// ── JavaScript / TypeScript ──
	{"javascript", regexp.MustCompile(`(?m)^\s*(const|let|var)\s+\w+\s*=`), 3},
	{"javascript", regexp.MustCompile(`(?m)^\s*(async\s+)?function\s+\w*\s*\(`), 3},
	{"javascript", regexp.MustCompile(`\bawait\s+\w+`), 2},
	{"javascript", regexp.MustCompile(`\bconsole\.(log|error|warn)\(`), 2},
	{"javascript", regexp.MustCompile(`\brequire\(['"]`), 2},
	{"javascript", regexp.MustCompile(`\bfrom\s+['"][^'"]+['"]`), 2},
	{"javascript", regexp.MustCompile(`(?m)^\s*case\s+['"][^'"]+['"]\s*:`), 2},
	{"javascript", regexp.MustCompile(`=>\s*[{(]`), 2},
	{"javascript", regexp.MustCompile(`\bexport\s+(default|const|function|class)`), 2},
	{"javascript", regexp.MustCompile(`\bmodule\.exports`), 3},
	{"javascript", regexp.MustCompile(`\bJSON\.(parse|stringify)`), 2},
	{"javascript", regexp.MustCompile(`\bprocess\.env`), 2},
	{"javascript", regexp.MustCompile(`\bundefined\b`), 1},
	{"javascript", regexp.MustCompile(`(?m)^\s*(export\s+)?(interface|type)\s+\w+`), 3},
	{"javascript", regexp.MustCompile(`\b:\s*(string|number|boolean|any|void|unknown)\b`), 2},
	{"javascript", regexp.MustCompile(`\bimplements\s+\w+`), 2},
	{"javascript", regexp.MustCompile(`\bas\s+(const|any|string|number)\b`), 2},

	// ── Python ──
	{"python", regexp.MustCompile(`(?m)^\s*def\s+\w+\s*\(`), 4},
	{"python", regexp.MustCompile(`(?m)^\s*import\s+\w+`), 3},
	{"python", regexp.MustCompile(`(?m)^\s*from\s+\w+\s+import`), 3},
	{"python", regexp.MustCompile(`(?m)^\s*class\s+\w+\s*:`), 3},
	{"python", regexp.MustCompile(`(?m)^\s*if\s+__name__\s*==`), 4},
	{"python", regexp.MustCompile(`(?m)^\s*print\(`), 2},
	{"python", regexp.MustCompile(`(?m)^\s*@\w+`), 2},
	{"python", regexp.MustCompile(`(?m)^\s*except\s*:`), 3},
	{"python", regexp.MustCompile(`(?m)^\s*elif\s`), 2},
	{"python", regexp.MustCompile(`(?m)^\s*self\.`), 2},
	{"python", regexp.MustCompile(`#!.*python`), 5},
	{"python", regexp.MustCompile(`(?m)^\s*with\s+\w+\(`), 2},
	{"python", regexp.MustCompile(`(?m)^\s*for\s+\w+\s+in\s+\w+:`), 2},

	// ── Go ──
	{"go", regexp.MustCompile(`(?m)^package\s+\w+`), 5},
	{"go", regexp.MustCompile(`(?m)^func\s+\(?\w`), 4},
	{"go", regexp.MustCompile(`(?m)^import\s+\(`), 3},
	{"go", regexp.MustCompile(`\bdefer\s+\w+\(`), 3},
	{"go", regexp.MustCompile(`\bfmt\.(Println|Printf|Sprintf)\(`), 3},
	{"go", regexp.MustCompile(`(?m)^\s*type\s+\w+\s+struct`), 3},
	{"go", regexp.MustCompile(`\bchan\s+\w+`), 2},
	{"go", regexp.MustCompile(`\binterface\{\}`), 2},
	{"go", regexp.MustCompile(`(?m)^\s*go\s+func\(`), 2},
	{"go", regexp.MustCompile(`\berr\s*:?=\s*`), 2},

	// ── PHP ──
	{"php", regexp.MustCompile(`<\?php`), 5},
	{"php", regexp.MustCompile(`\bnamespace\s+\w+`), 3},
	{"php", regexp.MustCompile(`(?m)^\s*function\s+\w+\s*\([^)]*\$`), 3},
	{"php", regexp.MustCompile(`\b(public|private|protected)\s+function`), 2},
	{"php", regexp.MustCompile(`\w->\w`), 2},
	{"php", regexp.MustCompile(`\$(GET|POST|SESSION|SERVER|REQUEST|COOKIE)\b`), 2},
	{"php", regexp.MustCompile(`\b(echo|print_r|var_dump)\s*\(`), 2},
	{"php", regexp.MustCompile(`\buse\s+\w+\\`), 2},
	{"php", regexp.MustCompile(`\$\w+\s*=`), 1},

	// ── HTML ──
	{"html", regexp.MustCompile(`(?i)<!DOCTYPE\s+html`), 5},
	{"html", regexp.MustCompile(`(?i)<html[ >]`), 4},
	{"html", regexp.MustCompile(`(?i)<body[ >]`), 3},
	{"html", regexp.MustCompile(`(?i)<head[ >]`), 3},
	{"html", regexp.MustCompile(`(?i)<script[ >]`), 2},
	{"html", regexp.MustCompile(`(?i)<style[ >]`), 2},
	{"html", regexp.MustCompile(`(?i)<meta\s`), 2},
	{"html", regexp.MustCompile(`(?i)<div[ >]`), 2},
	{"html", regexp.MustCompile(`(?i)<link\s`), 2},

	// ── CSS ──
	{"css", regexp.MustCompile(`(?m)^\s*[.#][\w-]+\s*\{`), 3},
	{"css", regexp.MustCompile(`(?m)^\s*[.#]?[\w-]+\s*\{`), 2},
	{"css", regexp.MustCompile(`(?m)@media`), 3},
	{"css", regexp.MustCompile(`(?m):root\s*\{`), 3},
	{"css", regexp.MustCompile(`(?m)^\s*--[\w-]+\s*:`), 2},
	{"css", regexp.MustCompile(`(?m)^\s*\w[\w-]*\s*:\s*[^;{}]+;`), 2},

	// ── SQL ──
	{"sql", regexp.MustCompile(`(?i)\bSELECT\b[\s\S]{0,80}\bFROM\b`), 4},
	{"sql", regexp.MustCompile(`(?i)\bCREATE\s+TABLE\b`), 4},
	{"sql", regexp.MustCompile(`(?i)\bINSERT\s+INTO\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bUPDATE\s+\w+\s+SET\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bDELETE\s+FROM\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bALTER\s+TABLE\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bJOIN\s+\w+\s+ON\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bPRIMARY\s+KEY\b`), 3},
	{"sql", regexp.MustCompile(`(?i)\bGROUP\s+BY\b`), 2},
	{"sql", regexp.MustCompile(`(?i)\bORDER\s+BY\b`), 2},
	{"sql", regexp.MustCompile(`(?i)\bSELECT\s+\*`), 3},

	// ── C ──
	{"c", regexp.MustCompile(`(?m)^\s*#include\s*<`), 4},
	{"c", regexp.MustCompile(`(?m)^\s*#define\b`), 3},
	{"c", regexp.MustCompile(`(?m)^\s*#ifdef\b`), 3},
	{"c", regexp.MustCompile(`(?m)^\s*int\s+main\s*\(`), 4},
	{"c", regexp.MustCompile(`\bmalloc\s*\(`), 3},
	{"c", regexp.MustCompile(`\bfree\s*\(`), 2},
	{"c", regexp.MustCompile(`\bprintf\s*\(`), 2},
	{"c", regexp.MustCompile(`\bstruct\s+\w+\s*\{`), 2},

	// ── C++ ──
	{"cpp", regexp.MustCompile(`\bstd::`), 4},
	{"cpp", regexp.MustCompile(`\busing\s+namespace\b`), 4},
	{"cpp", regexp.MustCompile(`(?m)^\s*#include\s*<iostream`), 5},
	{"cpp", regexp.MustCompile(`\bcout\s*<<|cin\s*>>`), 4},
	{"cpp", regexp.MustCompile(`\bvector<|map<|unordered_map<`), 2},
	{"cpp", regexp.MustCompile(`(?m)^\s*(public|private|protected):\s*$`), 3},
	{"cpp", regexp.MustCompile(`\bnullptr\b`), 3},
	{"cpp", regexp.MustCompile(`\bconstexpr\b`), 3},

	// ── Java ──
	{"java", regexp.MustCompile(`(?m)^\s*public\s+class\s+\w+`), 4},
	{"java", regexp.MustCompile(`(?m)^\s*import\s+java\.`), 4},
	{"java", regexp.MustCompile(`\bSystem\.out\.(print|println)`), 3},
	{"java", regexp.MustCompile(`\b@Override\b`), 3},
	{"java", regexp.MustCompile(`\bstatic\s+void\s+main`), 4},
	{"java", regexp.MustCompile(`\bnew\s+Scanner\b`), 2},
	{"java", regexp.MustCompile(`\bthrows\s+\w+`), 2},
	{"java", regexp.MustCompile(`\bString\[\]\s+args`), 2},

	// ── Rust ──
	{"rust", regexp.MustCompile(`(?m)^\s*fn\s+\w+`), 4},
	{"rust", regexp.MustCompile(`\blet\s+mut\s+\w+`), 3},
	{"rust", regexp.MustCompile(`\bimpl\s+\w+`), 3},
	{"rust", regexp.MustCompile(`\bprintln!\(`), 3},
	{"rust", regexp.MustCompile(`\buse\s+\w+::`), 3},
	{"rust", regexp.MustCompile(`\bResult<|Option<`), 2},
	{"rust", regexp.MustCompile(`\bmatch\s+\w+\s*\{`), 2},
	{"rust", regexp.MustCompile(`\bcargo\b`), 2},

	// ── Bash / Shell ──
	{"bash", regexp.MustCompile(`#!.*(bash|sh)`), 5},
	{"bash", regexp.MustCompile(`(?m)^\s*fi\s*$`), 3},
	{"bash", regexp.MustCompile(`(?m)^\s*then\s*$`), 2},
	{"bash", regexp.MustCompile(`(?m)^\s*if\s+\[`), 2},
	{"bash", regexp.MustCompile(`(?m)^\s*case\s+\$\w+\s+in`), 3},
	{"bash", regexp.MustCompile(`(?m)^\s*export\s+\w+=`), 2},
	{"bash", regexp.MustCompile(`(?m)^\s*(sudo|apt|yum|dnf|npm|yarn|git|docker|systemctl|chmod|chown|curl|wget|grep|sed|awk|tar|unzip|pip)\s`), 2},
	{"bash", regexp.MustCompile(`(?m)^\s*echo\s+["']`), 2},
	{"bash", regexp.MustCompile(`(?m)^\s*for\s+\w+\s+in\s+`), 2},

	// ── JSON ──
	{"json", regexp.MustCompile(`(?m)^\s*"[^"]+"\s*:\s*[\[{"\d]`), 3},
	{"json", regexp.MustCompile(`(?m)^\s*"[^"]+"\s*:\s*[^,}]+,\s*$`), 2},
	{"json", regexp.MustCompile(`(?m)^\s*\{$`), 2},
	{"json", regexp.MustCompile(`(?m)^\s*\}\s*$`), 1},
	{"json", regexp.MustCompile(`(?m)^\s*\[\s*$`), 1},

	// ── YAML ──
	{"yaml", regexp.MustCompile(`(?m)^---\s*$`), 3},
	{"yaml", regexp.MustCompile(`(?m)^\s*[\w.-]+\s*:\s*$`), 2},
	{"yaml", regexp.MustCompile(`(?m)^\s{2,}[\w.-]+\s*:`), 2},
	{"yaml", regexp.MustCompile(`(?m)^\s*-\s+[\w"']`), 2},
	{"yaml", regexp.MustCompile(`(?m)^\s*[\w.-]+\s*:\s*["'|>]`), 2},

	// ── XML ──
	{"xml", regexp.MustCompile(`<\?xml`), 5},
	{"xml", regexp.MustCompile(`(?i)<!DOCTYPE\s+\w+`), 3},
	{"xml", regexp.MustCompile(`(?i)<\w+[^>]*\sxmlns`), 3},
	{"xml", regexp.MustCompile(`(?i)<\w+[^>]*>\s*</\w+>`), 2},
	{"xml", regexp.MustCompile(`(?i)<\w+[^>]*/>`), 2},

	// ── Dockerfile ──
	{"dockerfile", regexp.MustCompile(`(?m)^FROM\s+\w+`), 5},
	{"dockerfile", regexp.MustCompile(`(?m)^RUN\s`), 3},
	{"dockerfile", regexp.MustCompile(`(?m)^CMD\s`), 3},
	{"dockerfile", regexp.MustCompile(`(?m)^ENTRYPOINT\s`), 3},
	{"dockerfile", regexp.MustCompile(`(?m)^COPY\s`), 3},
	{"dockerfile", regexp.MustCompile(`(?m)^WORKDIR\s`), 2},
	{"dockerfile", regexp.MustCompile(`(?m)^EXPOSE\s`), 2},
	{"dockerfile", regexp.MustCompile(`(?m)^ENV\s`), 2},

	// ── Markdown ──
	{"markdown", regexp.MustCompile(`(?m)^#{1,6}\s`), 3},
	{"markdown", regexp.MustCompile(`(?m)^\x60{3}|^~~~`), 2},
	{"markdown", regexp.MustCompile(`(?m)^\*\*[^*]+\*\*`), 2},
	{"markdown", regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`), 2},
	{"markdown", regexp.MustCompile(`(?m)^>\s`), 1},
	{"markdown", regexp.MustCompile(`(?m)^-\s`), 1},

	// ── INI / TOML ──
	{"ini", regexp.MustCompile(`(?m)^\[[\w.-]+\]\s*$`), 3},
	{"ini", regexp.MustCompile(`(?m)^\w+\s*=\s*["']?[\w./-]+["']?\s*$`), 1},

	// ── Kotlin ──
	{"kotlin", regexp.MustCompile(`(?m)^\s*fun\s+\w+`), 3},
	{"kotlin", regexp.MustCompile(`\bval\s+\w+\s*:`), 2},
	{"kotlin", regexp.MustCompile(`\bvar\s+\w+\s*:`), 2},
	{"kotlin", regexp.MustCompile(`\bprintln\(`), 2},
	{"kotlin", regexp.MustCompile(`(?m)^\s*package\s+\w+`), 2},

	// ── Swift ──
	{"swift", regexp.MustCompile(`(?m)^\s*import\s+(UIKit|Foundation|SwiftUI)`), 4},
	{"swift", regexp.MustCompile(`(?m)^\s*func\s+\w+`), 2},
	{"swift", regexp.MustCompile(`(?m)^\s*var\s+\w+\s*[:=]`), 2},
	{"swift", regexp.MustCompile(`\bprint\(`), 1},

	// ── Ruby ──
	{"ruby", regexp.MustCompile(`(?m)^\s*def\s+\w+\s*$`), 3},
	{"ruby", regexp.MustCompile(`(?m)^\s*end\s*$`), 3},
	{"ruby", regexp.MustCompile(`(?m)^\s*attr_(reader|writer|accessor)`), 3},
	{"ruby", regexp.MustCompile(`(?m)^\s*def\s+self\.`), 3},
	{"ruby", regexp.MustCompile(`(?m)^\s*require\s+['"]`), 2},
	{"ruby", regexp.MustCompile(`\bputs\s+`), 2},
	{"ruby", regexp.MustCompile(`(?m)^\s*class\s+\w+\s*$`), 2},

	// ── Lua ──
	{"lua", regexp.MustCompile(`(?m)^\s*local\s+\w+\s*=`), 3},
	{"lua", regexp.MustCompile(`(?m)^\s*function\s+\w+\(`), 2},
	{"lua", regexp.MustCompile(`(?m)^\s*require\s*\(`), 2},
	{"lua", regexp.MustCompile(`\bprint\(`), 1},

	// ── C# ──
	{"csharp", regexp.MustCompile(`(?m)^\s*using\s+System`), 4},
	{"csharp", regexp.MustCompile(`\bConsole\.(WriteLine|Write)`), 3},
	{"csharp", regexp.MustCompile(`(?m)^\s*namespace\s+\w+`), 3},
	{"csharp", regexp.MustCompile(`\bstring\[\]\s+args`), 2},
	{"csharp", regexp.MustCompile(`\bvar\s+\w+\s*=\s*new`), 2},

	// ── PowerShell ──
	{"powershell", regexp.MustCompile(`(?m)^\s*Write-Host`), 3},
	{"powershell", regexp.MustCompile(`(?m)^\s*(Get|Set|New|Remove|Add|Invoke)-`), 2},
	{"powershell", regexp.MustCompile(`(?m)^\s*param\s*\(`), 2},
	{"powershell", regexp.MustCompile(`(?m)^\s*\$_\s*\|`), 2},

	// ── Batch ──
	{"batch", regexp.MustCompile(`(?m)^@echo\s+off`), 4},
	{"batch", regexp.MustCompile(`(?m)^\s*(echo|set|cd|dir|pause)\s`), 2},
	{"batch", regexp.MustCompile(`(?m)^\s*:\w+`), 2},

	// ── Nginx ──
	{"nginx", regexp.MustCompile(`(?m)^\s*server\s*\{`), 3},
	{"nginx", regexp.MustCompile(`(?m)^\s*location\s`), 3},
	{"nginx", regexp.MustCompile(`(?m)^\s*listen\s+\d+`), 3},
	{"nginx", regexp.MustCompile(`(?m)^\s*proxy_pass\s`), 3},

	// ── GraphQL ──
	{"graphql", regexp.MustCompile(`(?m)^\s*(query|mutation|subscription)\s+\w+`), 4},
	{"graphql", regexp.MustCompile(`(?m)^\s*type\s+\w+\s*\{`), 3},
	{"graphql", regexp.MustCompile(`(?m)^\s*schema\s*\{`), 3},
	{"graphql", regexp.MustCompile(`(?m)^\s*scalar\s+\w+`), 3},

	// ── Protobuf ──
	{"protobuf", regexp.MustCompile(`(?m)^\s*syntax\s*=\s*["']proto3`), 5},
	{"protobuf", regexp.MustCompile(`(?m)^\s*message\s+\w+\s*\{`), 4},
	{"protobuf", regexp.MustCompile(`(?m)^\s*service\s+\w+\s*\{`), 4},
	{"protobuf", regexp.MustCompile(`(?m)^\s*rpc\s+\w+\(`), 4},
	{"protobuf", regexp.MustCompile(`(?m)^\s*enum\s+\w+\s*\{`), 3},

	// ── Perl ──
	{"perl", regexp.MustCompile(`(?m)^\s*use\s+strict`), 3},
	{"perl", regexp.MustCompile(`(?m)^\s*my\s+\$\w+`), 3},
	{"perl", regexp.MustCompile(`(?m)^\s*sub\s+\w+`), 3},
	{"perl", regexp.MustCompile(`(?m)^\s*print\s+"`), 2},

	// ── VB ──
	{"vb", regexp.MustCompile(`(?m)^\s*(Dim|Sub|End\s+Sub|Function)\s`), 3},
	{"vb", regexp.MustCompile(`(?m)^\s*MsgBox\s`), 3},
}

// detectPasteLang — tebak bahasa kode dengan scoring sinyal di seluruh konten.
// Skor < 3 → dianggap teks biasa ("").
func detectPasteLang(content string) string {
	type langScore struct {
		score     int
		maxWeight int
	}
	scores := map[string]*langScore{}
	for _, s := range langSignals {
		if !s.re.MatchString(content) {
			continue
		}
		ls := scores[s.lang]
		if ls == nil {
			ls = &langScore{}
			scores[s.lang] = ls
		}
		ls.score += s.weight
		if s.weight > ls.maxWeight {
			ls.maxWeight = s.weight
		}
	}
	best, bestScore := "", 0
	for lang, ls := range scores {
		// Tie-break: skor sama → pilih yang punya sinyal paling spesifik
		if ls.score > bestScore ||
			(ls.score == bestScore && ls.maxWeight > scores[best].maxWeight) {
			best, bestScore = lang, ls.score
		}
	}
	if bestScore < 3 {
		return ""
	}
	return best
}

func handleGetPaste(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	raw := strings.TrimSpace(args)
	m := rePastebin.FindStringSubmatch(raw)
	if len(m) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"📋 *Get Pastebin*\n\n"+
				"> Ambil konten dari Pastebin\n\n"+
				"*Format:*\n"+
				"> `%sgetpaste <link pastebin>`\n\n"+
				"*Contoh:*\n"+
				"> `%sgetpaste https://pastebin.com/Gu8RZaqv`",
			Prefix, Prefix))
		return
	}
	reactMsg(ctx, evt, "📋")

	id := m[1]
	body, err := dlGetSafe("https://pastebin.com/raw/" + id)
	if err != nil || len(body) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil konten Pastebin.")
		return
	}
	content := string(body)
	truncated := false
	if len(content) > 4000 {
		content = content[:4000]
		truncated = true
	}

	b := NewAIRich().SetTitle("📋 Get Pastebin")
	if lang := detectPasteLang(content); lang != "" {
		b.AddCode(lang, content)
	} else {
		b.AddText(content)
	}
	footer := "Paste ID: " + id
	if truncated {
		footer += " · konten dipotong (terlalu panjang)"
	}
	b.SetFooter(footer)

	if _, err := b.Send(ctx, chat); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Cek Ganteng ──────────────────────────────────────────────────────────────
// Random persen — port dari cek/cekganteng.js.
// Cmd: !cekganteng [nama/@mention]

func handleCekGanteng(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	target := strings.TrimSpace(args)
	if target == "" {
		target = "kamu"
	}
	percent := time.Now().UnixNano() % 101
	desc := "Mungkin inner beauty? 🤭"
	switch {
	case percent >= 90:
		desc = "Ganteng maksimal! 😍🔥"
	case percent >= 70:
		desc = "Ganteng banget! 😎"
	case percent >= 50:
		desc = "Lumayan ganteng~ 👍"
	case percent >= 30:
		desc = "Biasa aja sih 😅"
	}
	sendText(ctx, chat, fmt.Sprintf(
		"😎 *Cek Ganteng*\n\n> %s: *%d%%* ganteng\n> %s", target, percent, desc))
}

// ─── Cek Bucin ────────────────────────────────────────────────────────────────
// Random persen — port dari cek/cekbucin.js.
// Cmd: !cekbucin [nama/@mention]

func handleCekBucin(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	target := strings.TrimSpace(args)
	if target == "" {
		target = "kamu"
	}
	percent := time.Now().UnixNano() % 101
	desc := "Santai aja, gak bucin 😎"
	switch {
	case percent >= 90:
		desc = "BUCIN AKUT! Udah gabisa diselamatkan 😭💔"
	case percent >= 70:
		desc = "Bucin parah nih~ 🥺"
	case percent >= 50:
		desc = "Lumayan bucin 💕"
	case percent >= 30:
		desc = "Sedikit bucin 😊"
	}
	sendText(ctx, chat, fmt.Sprintf(
		"💕 *Cek Bucin*\n\n> %s: *%d%%* bucin\n> %s", target, percent, desc))
}

// ─── Roblox Stalk ─────────────────────────────────────────────────────────────
// API Roblox publik (tanpa key) — port dari stalker/robloxstalk.js.
// Cmd: !robloxstalk <username>, !rblxstalk, !rbxstalk

type robloxUser struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Created     string `json:"created"`
	IsBanned    bool   `json:"isBanned"`
}

func handleRobloxStalk(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	username := strings.TrimSpace(args)
	if username == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%srobloxstalk Linkmon99`", Prefix))
		return
	}
	reactMsg(ctx, evt, "🔍")

	// 1. Cari user.
	searchURL := "https://users.roblox.com/v1/users/search?keyword=" + url.QueryEscape(username) + "&limit=10"
	body, err := dlGetSafe(searchURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal menghubungi API Roblox.")
		return
	}
	var searchResp struct {
		Data []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &searchResp) != nil || len(searchResp.Data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ User *%s* tidak ditemukan di Roblox.", username))
		return
	}
	userID := searchResp.Data[0].ID

	// 2. Detail user.
	detailBody, err := dlGetSafe(fmt.Sprintf("https://users.roblox.com/v1/users/%d", userID))
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal mengambil detail user.")
		return
	}
	var user robloxUser
	if json.Unmarshal(detailBody, &user) != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Data user tidak valid.")
		return
	}

	// 3. Avatar.
	avatarURL := ""
	avBody, err := dlGetSafe(fmt.Sprintf(
		"https://thumbnails.roblox.com/v1/users/avatar?userIds=%d&size=420x420&format=Png", userID))
	if err == nil {
		var av struct {
			Data []struct {
				ImageURL string `json:"imageUrl"`
			} `json:"data"`
		}
		if json.Unmarshal(avBody, &av) == nil && len(av.Data) > 0 {
			avatarURL = av.Data[0].ImageURL
		}
	}

	// 4. Followers / following / friends.
	var followers, following, friends int
	if b, err := dlGetSafe(fmt.Sprintf("https://friends.roblox.com/v1/users/%d/followers/count", userID)); err == nil {
		var c struct {
			Count int `json:"count"`
		}
		json.Unmarshal(b, &c)
		followers = c.Count
	}
	if b, err := dlGetSafe(fmt.Sprintf("https://friends.roblox.com/v1/users/%d/followings/count", userID)); err == nil {
		var c struct {
			Count int `json:"count"`
		}
		json.Unmarshal(b, &c)
		following = c.Count
	}
	if b, err := dlGetSafe(fmt.Sprintf("https://friends.roblox.com/v1/users/%d/friends/count", userID)); err == nil {
		var c struct {
			Count int `json:"count"`
		}
		json.Unmarshal(b, &c)
		friends = c.Count
	}

	display := user.DisplayName
	if display == "" {
		display = user.Name
	}
	bio := user.Description
	if bio == "" {
		bio = "-"
	}
	created := user.Created
	if len(created) >= 10 {
		created = created[:10]
	}

	// Dua varian: plain untuk caption media, rich untuk fallback sendText.
	plain := fmt.Sprintf(
		"🎮 Roblox Stalk\n\n"+
			"👤 Username: %s\n"+
			"📛 Display: %s\n"+
			"🆔 User ID: %d\n"+
			"📅 Bergabung: %s\n"+
			"👥 Followers: %s\n"+
			"👣 Following: %s\n"+
			"🤝 Friends: %s\n\n"+
			"📝 Bio:\n%s\n\n"+
			"🔗 https://www.roblox.com/users/%d/profile",
		user.Name, display, user.ID, created,
		shortNum(followers), shortNum(following), shortNum(friends),
		bio, user.ID)
	rich := fmt.Sprintf(
		"🎮 *Roblox Stalk*\n\n"+
			"👤 *Username:* %s\n"+
			"📛 *Display:* %s\n"+
			"🆔 *User ID:* %d\n"+
			"📅 *Bergabung:* %s\n"+
			"👥 *Followers:* %s\n"+
			"👣 *Following:* %s\n"+
			"🤝 *Friends:* %s\n\n"+
			"📝 *Bio:*\n%s\n\n"+
			"🔗 https://www.roblox.com/users/%d/profile",
		user.Name, display, user.ID, created,
		shortNum(followers), shortNum(following), shortNum(friends),
		bio, user.ID)

	if avatarURL != "" {
		if img, err := dlGetSafe(avatarURL); err == nil && len(img) > 100 {
			if sendImage(ctx, chat, img, plain) == nil {
				reactMsg(ctx, evt, "✅")
				return
			}
		}
	}
	sendText(ctx, chat, rich)
	reactMsg(ctx, evt, "✅")
}

// ─── Jodoh ────────────────────────────────────────────────────────────────────
// Random kecocokan — port dari fun/jodoh.js (versi SC: tanpa DB/registrasi,
// tanpa butuh group metadata; cukup 2 nama dari argumen).
// Cmd: !jodoh <nama1> <nama2>

var loveQuotes = []string{
	"Cinta sejati tidak pernah mengenal jarak 💕",
	"Dua hati yang bersatu takkan terpisahkan 💗",
	"Kalian seperti puzzle yang sempurna 🧩",
	"Match made in heaven! ✨",
	"Chemistry-nya kuat banget! 🔥",
	"Couple goals banget sih kalian 💑",
	"Destiny brought you together 🌟",
	"Perfect match detected! 💘",
}

func handleJodoh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	fields := strings.Fields(args)
	if len(fields) < 2 {
		sendText(ctx, chat, fmt.Sprintf(
			"💘 *Jodoh*\n\n"+
				"> Cek kecocokan dua nama\n\n"+
				"*Format:*\n"+
				"> `%sjodoh <nama1> <nama2>`\n\n"+
				"*Contoh:*\n"+
				"> `%sjodoh Budi Siti`",
			Prefix, Prefix))
		return
	}
	name1 := fields[0]
	name2 := fields[1]

	percent := time.Now().UnixNano() % 101
	emoji := "💕"
	label := "Butuh Usaha Lebih 💔"
	switch {
	case percent >= 90:
		emoji, label = "💕💕💕💕💕", "JODOH SEJATI! 💍"
	case percent >= 70:
		emoji, label = "💕💕💕💕", "Sangat Cocok! 💖"
	case percent >= 50:
		emoji, label = "💕💕💕", "Lumayan Cocok 💗"
	case percent >= 30:
		emoji, label = "💕💕", "Bisa Dicoba 💓"
	}
	quote := loveQuotes[time.Now().UnixNano()%int64(len(loveQuotes))]

	sendText(ctx, chat, fmt.Sprintf(
		"💘 *Jodoh*\n\n"+
			"> 💑 %s ❤️ %s\n\n"+
			"> *Kecocokan:* %d%%\n"+
			"> %s %s\n\n"+
			"> _%s_",
		name1, name2, percent, emoji, label, quote))
}
