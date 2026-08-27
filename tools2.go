package main

// tools2.go — plugin tools lanjutan (26 tools)
// Port dari Anya MD plugins ke Go.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ─── Barcode ──────────────────────────────────────────────────────────────────
// API: barcodeapi.org — Port dari tools-barcode.js
// Cmd: !barcode

func handleBarcode(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sbarcode 1234567890`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	data, err := dlGet("https://barcodeapi.org/api/128/"+url.PathEscape(text), nil)
	if err != nil || len(data) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat barcode.")
		return
	}
	if err := sendImage(ctx, chat, data, "📊 Barcode: "+text); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Blur ─────────────────────────────────────────────────────────────────────
// API: uguu.se upload + popcat.xyz/blur — Port dari tools-blur.js
// Cmd: !blur

func uguuUpload(data []byte, filename string) (string, error) {
	var body bytes.Buffer
	boundary := uuid.New().String()
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(fmt.Sprintf(`Content-Disposition: form-data; name="files[]"; filename="%s"`+"\r\n", filename))
	body.WriteString("Content-Type: image/jpeg\r\n\r\n")
	body.Write(data)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, _ := http.NewRequest("POST", "https://uguu.se/upload.php", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("User-Agent", "Mozilla/5.0 Android Chrome/147")
	resp, err := dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var res struct {
		Success bool `json:"success"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || !res.Success || len(res.Files) == 0 {
		return "", fmt.Errorf("upload gagal")
	}
	return res.Files[0].URL, nil
}

func handleBlur(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu, lalu ketik `%sblur`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download gambar.")
		return
	}
	imgURL, err := uguuUpload(imgData, "image.jpg")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	blurURL := "https://api.popcat.xyz/v2/blur?image=" + url.QueryEscape(imgURL)
	result, err := dlGet(blurURL, nil)
	if err != nil || len(result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal blur gambar.")
		return
	}
	if err := sendImage(ctx, chat, result, "🌫️ Blur"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Caption ─────────────────────────────────────────────────────────────────
// API: uguu.se + popcat.xyz/caption — Port dari tools-cap.js
// Cmd: !cap <teks>

func handleCaption(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar + ketik `%scap <teks>`", Prefix))
		return
	}
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%scap Teks caption di sini`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download gambar.")
		return
	}
	imgURL, err := uguuUpload(imgData, "image.jpg")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	capURL := fmt.Sprintf("https://api.popcat.xyz/v2/caption?image=%s&text=%s&bottom=false&dark=true&fontsize=30",
		url.QueryEscape(imgURL), url.QueryEscape(text))
	result, err := dlGet(capURL, nil)
	if err != nil || len(result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal tambah caption.")
		return
	}
	if err := sendImage(ctx, chat, result, "✏️ Caption: "+text); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Compress Image ───────────────────────────────────────────────────────────
// Local ffmpeg — Port dari tools-compress.js
// Cmd: !compress

func handleCompress(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar atau video dulu, lalu ketik `%scompress`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	uid := uuid.New().String()
	_ = os.MkdirAll("temp", 0o755)

	// Video
	if vid := quoted.GetVideoMessage(); vid != nil {
		rawBytes, err := waClient.Download(ctx, vid)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		inPath := filepath.Join("temp", "comp_in_"+uid+".mp4")
		outPath := filepath.Join("temp", "comp_out_"+uid+".mp4")
		defer os.Remove(inPath)
		defer os.Remove(outPath)
		_ = os.WriteFile(inPath, rawBytes, 0o644)
		var stderr bytes.Buffer
		cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
			"-i", inPath, "-vf", "scale=640:-2",
			"-c:v", "libx264", "-crf", "28", "-preset", "fast",
			"-c:a", "aac", "-b:a", "96k", outPath)
		cmd.Stderr = &stderr
		// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
		if err := runCmdTimeout(cmd, 90*time.Second); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ ffmpeg: "+lastLines(stderr.String(), 2))
			return
		}
		outData, _ := os.ReadFile(outPath)
		caption := fmt.Sprintf("🗜️ Compressed\nSebelum: %.1f KB → Sesudah: %.1f KB",
			float64(len(rawBytes))/1024, float64(len(outData))/1024)
		if err := sendVideo(ctx, chat, outData, caption, "video/mp4"); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		reactMsg(ctx, evt, "✅")
		return
	}

	// Image
	if img := quoted.GetImageMessage(); img != nil {
		rawBytes, err := waClient.Download(ctx, img)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		inPath := filepath.Join("temp", "comp_in_"+uid+".jpg")
		outPath := filepath.Join("temp", "comp_out_"+uid+".jpg")
		defer os.Remove(inPath)
		defer os.Remove(outPath)
		_ = os.WriteFile(inPath, rawBytes, 0o644)
		var stderr bytes.Buffer
		cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
			"-i", inPath, "-vf", "scale=480:-2",
			"-q:v", "15", outPath)
		cmd.Stderr = &stderr
		// Fix: timeout — ffmpeg dengan input rusak bisa hang selamanya.
		if err := runCmdTimeout(cmd, 60*time.Second); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ ffmpeg: "+lastLines(stderr.String(), 2))
			return
		}
		outData, _ := os.ReadFile(outPath)
		caption := fmt.Sprintf("🗜️ Compressed\nSebelum: %.1f KB → Sesudah: %.1f KB",
			float64(len(rawBytes))/1024, float64(len(outData))/1024)
		if err := sendImage(ctx, chat, outData, caption); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		reactMsg(ctx, evt, "✅")
		return
	}

	sendText(ctx, chat, "❌ Hanya mendukung gambar atau video.")
}

// ─── HD Image ─────────────────────────────────────────────────────────────────
// API: imglarger.com (UploadNew + CheckStatusNew) — Port dari tools-hd.js
// Cmd: !hd

// imgLargerUploadResp — respon /api/legacy/upload (endpoint proxy resmi
// imgupscaler.com). Endpoint lama get1.imglarger.com/api/UpscalerNew/*
// sudah mati untuk user anonim: upload diterima tapi job mentok "waiting"
// selamanya — itu penyebab !hd selalu timeout sebelumnya.
type imgLargerUploadResp struct {
	TaskID string `json:"taskId"`
}

type imgLargerStatusResp struct {
	Status       string `json:"status"`
	DownloadURLs []string `json:"downloadUrls"`
	// Fallback ke bentuk lama (nested "data") kalau proxy mengubah format.
	Data struct {
		Status       string   `json:"status"`
		DownloadURLs []string `json:"downloadUrls"`
	} `json:"data"`
}

// imgUpscaler — port dari ImgUpscaler (tools-hd.js)
type imgUpscaler struct {
	uploadURL string
	statusURL string
	agent     string
}

func newImgUpscaler() *imgUpscaler {
	return &imgUpscaler{
		uploadURL: "https://imgupscaler.com/api/legacy/upload",
		statusURL: "https://imgupscaler.com/api/legacy/status",
		agent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
	}
}

// process — upload gambar lalu poll status sampai selesai.
// Meniru alur web imgupscaler.com: POST /api/legacy/upload (FormData:
// tool/mode/scaleRadio/file) → taskId, lalu poll /api/legacy/status
// 30× tiap 10 detik (±5 menit budget, sama seperti frontend resmi).
func (u *imgUpscaler) process(buffer []byte, scale int) (string, error) {
	if len(buffer) == 0 {
		return "", fmt.Errorf("Image buffer diperlukan")
	}

	// ── Upload ──
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="upload.png"`)
	h.Set("Content-Type", "image/png")
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(buffer); err != nil {
		return "", err
	}
	if err := mw.WriteField("tool", "upscaler"); err != nil {
		return "", err
	}
	if err := mw.WriteField("mode", "batch"); err != nil {
		return "", err
	}
	if err := mw.WriteField("scaleRadio", fmt.Sprintf("%d", scale)); err != nil {
		return "", err
	}
	mw.Close()

	req, err := http.NewRequest("POST", u.uploadURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "https://imgupscaler.com")
	req.Header.Set("Referer", "https://imgupscaler.com/")
	req.Header.Set("User-Agent", u.agent)

	resp, err := dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var uploadRes imgLargerUploadResp
	if err := json.Unmarshal(raw, &uploadRes); err != nil || uploadRes.TaskID == "" {
		return "", fmt.Errorf("gagal upload gambar: %s", string(raw))
	}
	taskID := uploadRes.TaskID

	// Beri jeda sebelum poll pertama — persis perilaku frontend (sleep 10s).
	time.Sleep(10 * time.Second)

	// ── Poll status ──
	for i := 0; i < 30; i++ {
		payload, _ := json.Marshal(map[string]any{
			"tool":       "upscaler",
			"taskId":     taskID,
			"scaleRadio": scale,
		})
		req, err := http.NewRequest("POST", u.statusURL, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://imgupscaler.com")
		req.Header.Set("Referer", "https://imgupscaler.com/")
		req.Header.Set("User-Agent", u.agent)

		resp, err := dlClient.Do(req)
		if err != nil {
			return "", err
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		var statusRes imgLargerStatusResp
		if err := json.Unmarshal(raw, &statusRes); err != nil {
			return "", fmt.Errorf("response status tidak valid: %s", string(raw))
		}
		status := statusRes.Status
		urls := statusRes.DownloadURLs
		if status == "" { // fallback bentuk nested lama
			status = statusRes.Data.Status
			urls = statusRes.Data.DownloadURLs
		}
		if status == "success" {
			if len(urls) == 0 || urls[0] == "" ||
				strings.HasPrefix(urls[0], "https://imgupscaler.com/results/") {
				return "", fmt.Errorf("downloadUrls kosong")
			}
			return urls[0], nil
		}
		time.Sleep(10 * time.Second)
	}
	return "", fmt.Errorf("Timeout upscale — layanan sibuk, coba lagi nanti")
}

func handleHD(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu, lalu ketik `%shd`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download.")
		return
	}

	hdURL, err := newImgUpscaler().process(imgData, 4)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upscale: "+err.Error())
		return
	}

	result, err := dlGet(hdURL, nil)
	if err != nil || len(result) < 100 {
		reactMsg(ctx, evt, "⚠️")
		sendText(ctx, chat, "⚠️ Gagal mengambil hasil upscale. Coba lagi nanti.")
		return
	}
	if err := sendImage(ctx, chat, result, "✨ HD Upscaled (4x)"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Remove Background ────────────────────────────────────────────────────────
// API: remove.bg — Port dari tools-removebg.js
// Cmd: !removebg, !rbg

func handleRemoveBG(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	if RemoveBGKey == "" {
		sendText(ctx, chat, "❌ RemoveBGKey belum diset di config.go\nDaftar gratis di https://remove.bg")
		return
	}
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply gambar dulu, lalu ketik `%sremovebg`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download.")
		return
	}

	var body bytes.Buffer
	boundary := uuid.New().String()
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="image_file"; filename="image.jpg"` + "\r\n")
	body.WriteString("Content-Type: image/jpeg\r\n\r\n")
	body.Write(imgData)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, _ := http.NewRequest("POST", "https://api.remove.bg/v1.0/removebg", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("X-Api-Key", RemoveBGKey)

	resp, err := dlClient.Do(req)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal request: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ API error: %d", resp.StatusCode))
		return
	}
	result, err := io.ReadAll(resp.Body)
	if err != nil || len(result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Response kosong.")
		return
	}
	if err := sendImage(ctx, chat, result, "🪄 Background Removed"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── ToFile ───────────────────────────────────────────────────────────────────
// Local — Port dari tools-tofile.js
// Cmd: !tofile <nama.ext>

var mimeByExt = map[string]string{
	".js": "text/javascript", ".ts": "text/plain", ".json": "application/json",
	".txt": "text/plain", ".md": "text/markdown", ".html": "text/html",
	".css": "text/css", ".xml": "application/xml", ".sql": "text/plain",
	".py": "text/x-python", ".go": "text/x-go", ".sh": "text/x-shellscript",
	".yaml": "text/yaml", ".yml": "text/yaml", ".csv": "text/csv",
	".pdf": "application/pdf", ".log": "text/plain", ".env": "text/plain",
}

func handleToFile(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	filename := strings.TrimSpace(args)
	if filename == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%stofile script.py`", Prefix))
		return
	}
	ext := strings.ToLower(filepath.Ext(filename))
	mimeType, ok := mimeByExt[ext]
	if !ok {
		exts, _ := mime.ExtensionsByType("application/octet-stream")
		_ = exts
		mimeType = "application/octet-stream"
	}

	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	var content []byte

	if quoted != nil {
		// Text
		if q := quoted.GetConversation(); q != "" {
			content = []byte(q)
		} else if q := quoted.GetExtendedTextMessage().GetText(); q != "" {
			content = []byte(q)
		} else if doc := quoted.GetDocumentMessage(); doc != nil {
			var err error
			content, err = waClient.Download(ctx, doc)
			if err != nil {
				reactMsg(ctx, evt, "❌")
				sendText(ctx, chat, "❌ Gagal download dokumen.")
				return
			}
			mimeType = doc.GetMimetype()
		}
	}
	if content == nil {
		// Teks dari pesan saat ini
		text := evt.Message.GetConversation()
		if text == "" {
			text = evt.Message.GetExtendedTextMessage().GetText()
		}
		// Strip command prefix
		if idx := strings.Index(text, " "); idx != -1 {
			remaining := strings.TrimSpace(text[idx:])
			if remaining != filename {
				content = []byte(remaining)
			}
		}
	}
	if len(content) == 0 {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply teks/dokumen dulu, lalu `%stofile %s`", Prefix, filename))
		return
	}

	reactMsg(ctx, evt, "⏳")
	uid := uuid.New().String()
	_ = os.MkdirAll("temp", 0o755)
	tmpPath := filepath.Join("temp", "tofile_"+uid+ext)
	defer os.Remove(tmpPath)
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal tulis file.")
		return
	}

	up, err := waClient.Upload(ctx, content, whatsmeow.MediaDocument)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(content))),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(filename),
		},
	})
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Upload GitHub ────────────────────────────────────────────────────────────
// API: GitHub Contents — Port dari tools-upgh.js
// Cmd: !uploadgh, !ghupload

func handleUpGH(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	if GithubToken == "" || GithubUser == "" || GithubRepo == "" {
		sendText(ctx, chat, "❌ GithubToken/GithubUser/GithubRepo belum diset di config.go")
		return
	}
	ci := msgContextInfo(evt)
	m := unwrapMsg(ci.GetQuotedMessage())
	if m == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply media dulu, lalu `%suploadgh [nama_file]`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	var data []byte
	var filename string
	var err error

	switch {
	case m.GetImageMessage() != nil:
		data, err = waClient.Download(ctx, m.GetImageMessage())
		filename = "image.jpg"
	case m.GetVideoMessage() != nil:
		data, err = waClient.Download(ctx, m.GetVideoMessage())
		filename = "video.mp4"
	case m.GetAudioMessage() != nil:
		data, err = waClient.Download(ctx, m.GetAudioMessage())
		filename = "audio.ogg"
	case m.GetDocumentMessage() != nil:
		data, err = waClient.Download(ctx, m.GetDocumentMessage())
		filename = m.GetDocumentMessage().GetFileName()
	default:
		sendText(ctx, chat, "❌ Tipe media tidak didukung.")
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download: "+err.Error())
		return
	}
	if args != "" {
		filename = strings.TrimSpace(args)
	}
	if filename == "" {
		filename = uuid.New().String()[:8] + ".bin"
	}

	// Fix path traversal: nama file dari user bisa berisi `../`, `/`, `\`
	// dan meng-overwrite file lain di repo (atau nulis ke path di luar
	// uploads/). Sanitasi: buang semua karakter bukan [A-Za-z0-9._-].
	filename = regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(filename, "_")
	if filename == "" || filename == "." || filename == ".." {
		filename = uuid.New().String()[:8] + ".bin"
	}

	path := fmt.Sprintf("uploads/%s_%s", time.Now().Format("20060102_150405"), filename)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", GithubUser, GithubRepo, path)

	payload, _ := json.Marshal(map[string]string{
		"message": "upload via YuukiBot",
		"content": base64.StdEncoding.EncodeToString(data),
		"branch":  "main",
	})
	req, _ := http.NewRequest("PUT", apiURL, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+GithubToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := dlClient.Do(req)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var res struct {
		Content struct {
			DownloadURL string `json:"download_url"`
		} `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&res)
	if res.Content.DownloadURL == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf("❌ Upload gagal (status %d)", resp.StatusCode))
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf("✅ *Uploaded ke GitHub!*\n\n🔗 %s", res.Content.DownloadURL))
}

// ─── Resend tanpa kompres ────────────────────────────────────────────────────
// Cmd: !resend
// Reply foto/video (atau kirim media + caption "!resend") → bot kirim ulang
// sebagai MEDIA biasa (foto bisa dilihat, video bisa diputar) — bukan dokumen.
// Bot tidak mengompres apa pun: bytes yang diterima dikirim ulang apa adanya.

func handleResend(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)

	// Media bisa dari quoted (reply) atau dari pesan ini sendiri (caption).
	m := unwrapMsg(ci.GetQuotedMessage())
	if m == nil {
		m = evt.Message
	}

	var data []byte
	var mimetype string
	var err error

	switch {
	case m.GetImageMessage() != nil:
		data, err = waClient.Download(ctx, m.GetImageMessage())
		mimetype = m.GetImageMessage().GetMimetype()
	case m.GetVideoMessage() != nil:
		data, err = waClient.Download(ctx, m.GetVideoMessage())
		mimetype = m.GetVideoMessage().GetMimetype()
	case m.GetAudioMessage() != nil:
		data, err = waClient.Download(ctx, m.GetAudioMessage())
		mimetype = m.GetAudioMessage().GetMimetype()
	case m.GetDocumentMessage() != nil:
		data, err = waClient.Download(ctx, m.GetDocumentMessage())
		mimetype = m.GetDocumentMessage().GetMimetype()
	default:
		sendText(ctx, chat, fmt.Sprintf("❌ Reply foto/video/dokumen dulu, lalu `%sresend`", Prefix))
		return
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download media: "+err.Error())
		return
	}

	reactMsg(ctx, evt, "⏳")
	switch {
	case m.GetImageMessage() != nil:
		err = sendImage(ctx, chat, data, "")
	case m.GetVideoMessage() != nil:
		err = sendVideo(ctx, chat, data, "", mimetype)
	case m.GetAudioMessage() != nil:
		err = sendAudio(ctx, chat, data, mimetype)
	case m.GetDocumentMessage() != nil:
		// Dokumen yang isinya foto/video/audio (mis. dikirim dari galeri
		// lewat tab Dokumen, atau HEIC) → kirim ulang sebagai MEDIA supaya
		// bisa dilihat/diputar. Dokumen beneran (pdf/zip/dll) tetap dokumen.
		switch {
		case strings.HasPrefix(mimetype, "image/"):
			err = sendImage(ctx, chat, data, "")
		case strings.HasPrefix(mimetype, "video/"):
			err = sendVideo(ctx, chat, data, "", mimetype)
		case strings.HasPrefix(mimetype, "audio/"):
			err = sendAudio(ctx, chat, data, mimetype)
		default:
			filename := m.GetDocumentMessage().GetFileName()
			if filename == "" {
				filename = resendFilename("file", mimetype)
			}
			up, uerr := waClient.Upload(ctx, data, whatsmeow.MediaDocument)
			if uerr != nil {
				err = uerr
				break
			}
			_, err = waClient.SendMessage(ctx, chat, &waE2E.Message{
				DocumentMessage: &waE2E.DocumentMessage{
					URL:           proto.String(up.URL),
					DirectPath:    proto.String(up.DirectPath),
					MediaKey:      up.MediaKey,
					FileEncSHA256: up.FileEncSHA256,
					FileSHA256:    up.FileSHA256,
					FileLength:    proto.Uint64(uint64(len(data))),
					Mimetype:      proto.String(mimetype),
					FileName:      proto.String(filename),
					ContextInfo:   mergeReplyCtx(ctx, newsletterCtxInfo(ctx)),
				},
			})
		}
	}
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// resendFilename — nama file untuk media yang dikompres WA (dokumen punya
// nama asli). Ekstensi diambil dari mimetype supaya nama file wajar.
func resendFilename(prefix, mimetype string) string {
	ext := map[string]string{
		"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp",
		"image/gif": "gif", "video/mp4": "mp4", "video/quicktime": "mov",
		"audio/ogg": "ogg", "audio/mpeg": "mp3", "audio/mp4": "m4a",
	}[mimetype]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("%s_%d.%s", prefix, time.Now().Unix(), ext)
}

// ─── Fetch URL ────────────────────────────────────────────────────────────────
// Port dari tools-fetch.js
// Cmd: !fetch <url>

func handleFetch(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	rawURL := strings.TrimSpace(args)
	if rawURL == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sfetch https://api.example.com/data`", Prefix))
		return
	}
	if !strings.HasPrefix(rawURL, "http") {
		rawURL = "https://" + rawURL
	}
	reactMsg(ctx, evt, "⏳")
	// Fix SSRF: dlGetSafe memblokir IP internal (127.0.0.1, 192.168.x.x,
	// 169.254.169.254 metadata cloud, dll) + cegah DNS rebinding + batasi ukuran.
	data, err := dlGetSafe(rawURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}

	// Coba parse sebagai JSON
	var jsonObj interface{}
	if err := json.Unmarshal(data, &jsonObj); err == nil {
		pretty, _ := json.MarshalIndent(jsonObj, "", "  ")
		text := string(pretty)
		if len(text) > 3000 {
			text = text[:3000] + "\n...(dipotong)"
		}
		sendText(ctx, chat, fmt.Sprintf("📄 *Fetch:* `%s`\n\n```%s```", rawURL, text))
	} else {
		text := string(data)
		if len(text) > 3000 {
			text = text[:3000] + "\n...(dipotong)"
		}
		sendText(ctx, chat, fmt.Sprintf("📄 *Fetch:* `%s`\n\n```%s```", rawURL, text))
	}
	reactMsg(ctx, evt, "✅")
}

// ─── TempMail ─────────────────────────────────────────────────────────────────
// API: tempail.top — Port dari tools-tempmail.js
// Cmd: !tempmail, !cekmail <token>

func handleTempMail(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	args = strings.TrimSpace(args)
	reactMsg(ctx, evt, "⏳")

	if args != "" {
		// Cek inbox
		body, err := dlGet("https://tempail.top/api/messages/"+args+"/ApiTempail", nil)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		var res struct {
			Mailbox  string `json:"mailbox"`
			Messages []struct {
				ID      string `json:"id"`
				From    string `json:"from"`
				Subject string `json:"subject"`
				Date    string `json:"date"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ Response tidak valid.")
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "📬 *Inbox:* `%s`\n", res.Mailbox)
		if len(res.Messages) == 0 {
			fmt.Fprintf(&b, "_Belum ada pesan_")
		} else {
			for i, m := range res.Messages {
				fmt.Fprintf(&b, "%d. *%s*\n   From: %s\n   %s\n", i+1, m.Subject, m.From, m.Date)
			}
		}
		sendText(ctx, chat, b.String())
		reactMsg(ctx, evt, "✅")
		return
	}

	// Buat email baru
	body, err := dlGet("https://tempail.top/api/email/create/ApiTempail", nil)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ "+err.Error())
		return
	}
	var res struct {
		Email      string `json:"email"`
		EmailToken string `json:"email_token"`
		DeletedIn  string `json:"deleted_in"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Email == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat email.")
		return
	}
	sendText(ctx, chat, fmt.Sprintf(
		"📧 *Temp Email Berhasil Dibuat!*\n\n"+
			"📬 Email: `%s`\n"+
			"🔑 Token: `%s`\n"+
			"⏱️ Expired: %s\n\n"+
			"Cek inbox: `%scekmail %s`",
		res.Email, res.EmailToken, res.DeletedIn, Prefix, res.EmailToken))
	reactMsg(ctx, evt, "✅")
}

// ─── Read View Once ───────────────────────────────────────────────────────────
// Port dari tools-readviewonce.js
// Cmd: !rvo, !read

func handleReadViewOnce(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply pesan view once dulu, lalu ketik `%srvo`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	// Unwrap view once
	m := quoted
	if vo := m.GetViewOnceMessage(); vo != nil && vo.Message != nil {
		m = vo.Message
	} else if vo := m.GetViewOnceMessageV2(); vo != nil && vo.Message != nil {
		m = vo.Message
	}

	if img := m.GetImageMessage(); img != nil {
		data, err := waClient.Download(ctx, img)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		_ = sendImage(ctx, chat, data, "👁️ View Once — dibuka")
		reactMsg(ctx, evt, "✅")
		return
	}
	if vid := m.GetVideoMessage(); vid != nil {
		data, err := waClient.Download(ctx, vid)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		_ = sendVideo(ctx, chat, data, "👁️ View Once — dibuka", "video/mp4")
		reactMsg(ctx, evt, "✅")
		return
	}
	if aud := m.GetAudioMessage(); aud != nil {
		data, err := waClient.Download(ctx, aud)
		if err != nil {
			reactMsg(ctx, evt, "❌")
			sendText(ctx, chat, "❌ "+err.Error())
			return
		}
		_ = sendAudio(ctx, chat, data, "audio/ogg; codecs=opus")
		reactMsg(ctx, evt, "✅")
		return
	}
	sendText(ctx, chat, "❌ Tidak ada media view once di pesan tersebut.")
}

// ─── Delete Pesan ─────────────────────────────────────────────────────────────
// Port dari tools-delete.js — hapus pesan yang di-reply
// Cmd: !del (sudah ada alias di commands.go untuk hapus antrian)
// Pakai !delmsg untuk ini

func handleDeleteMsg(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	if ci.GetStanzaID() == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply pesan yang mau dihapus, lalu `%sdelmsg`", Prefix))
		return
	}
	msgID := ci.GetStanzaID()
	participant := ci.GetParticipant()
	if participant == "" {
		participant = chat.String()
	}
	_, err := waClient.SendMessage(ctx, chat, waClient.BuildRevoke(chat,
		types.EmptyJID, msgID))
	_ = participant
	if err != nil {
		sendText(ctx, chat, "❌ Gagal hapus pesan: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── WhatMusic ────────────────────────────────────────────────────────────────
// API: doreso.com — Port dari tools-whatmusic.js
// Cmd: !whatmusic, !wmusic

func handleWhatMusic(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetAudioMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply audio/voice note dulu, lalu ketik `%swhatmusic`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")

	audioData, err := waClient.Download(ctx, quoted.GetAudioMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download audio.")
		return
	}

	var body bytes.Buffer
	boundary := uuid.New().String()
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="file"; filename="audio.ogg"` + "\r\n")
	body.WriteString("Content-Type: audio/ogg\r\n\r\n")
	body.Write(audioData)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, _ := http.NewRequest("POST", "https://api.doreso.com/humming", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Referer", "https://www.aha-music.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 Android Chrome/147")

	resp, err := dlClient.Do(req)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal identify: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var res struct {
		Data struct {
			Title   string `json:"title"`
			Artists string `json:"artists"`
			Acrid   string `json:"acrid"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Data.Title == "" {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Lagu tidak dikenali.")
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf(
		"🎵 *Lagu Teridentifikasi!*\n\n"+
			"🎼 Judul: *%s*\n"+
			"🎤 Artis: %s",
		res.Data.Title, res.Data.Artists))
}

// ─── Source Web ───────────────────────────────────────────────────────────────
// Port dari tools-source.js — tampilkan HTML source
// Cmd: !source <url>

func handleSource(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	rawURL := strings.TrimSpace(args)
	if rawURL == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%ssource https://example.com`", Prefix))
		return
	}
	if !strings.HasPrefix(rawURL, "http") {
		rawURL = "https://" + rawURL
	}
	reactMsg(ctx, evt, "⏳")
	// Fix SSRF: sama seperti !fetch — blokir IP internal + rebinding + cap ukuran.
	data, err := dlGetSafe(rawURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal fetch: "+err.Error())
		return
	}
	html := string(data)
	if len(html) > 3500 {
		html = html[:3500] + "\n...(dipotong)"
	}
	sendText(ctx, chat, fmt.Sprintf("🔍 *Source:* `%s`\n\n```%s```", rawURL, html))
	reactMsg(ctx, evt, "✅")
}

// ─── Cek ID Channel ───────────────────────────────────────────────────────────
// Port dari tools-cekidch.js
// Cmd: !cekidch, !idch

var reChannelCode = regexp.MustCompile(`whatsapp\.com/channel/([A-Za-z0-9_-]+)`)

func handleCekIDCh(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	link := strings.TrimSpace(args)
	if link == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sidch https://whatsapp.com/channel/xxx`", Prefix))
		return
	}
	m := reChannelCode.FindStringSubmatch(link)
	if len(m) < 2 {
		sendText(ctx, chat, "❌ Link channel tidak valid.")
		return
	}
	reactMsg(ctx, evt, "⏳")
	info, err := waClient.GetNewsletterInfoWithInvite(ctx, m[1])
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil info: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf(
		"📡 *Info Saluran*\n\n"+
			"📌 Nama: *%s*\n"+
			"🆔 ID: `%s`\n"+
			"👥 Subscriber: %d",
		info.ThreadMeta.Name.Text,
		info.ID.String(),
		info.ThreadMeta.SubscriberCount))
}

// ─── Cek ID Grup ──────────────────────────────────────────────────────────────
// Port dari tools-idgc.js
// Cmd: !cekidgc, !idgc

var reGroupCode = regexp.MustCompile(`chat\.whatsapp\.com/([A-Za-z0-9_-]+)`)

func handleCekIDGC(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	link := strings.TrimSpace(args)
	if link == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%sidgc https://chat.whatsapp.com/xxx`", Prefix))
		return
	}
	m := reGroupCode.FindStringSubmatch(link)
	if len(m) < 2 {
		sendText(ctx, chat, "❌ Link grup tidak valid.")
		return
	}
	reactMsg(ctx, evt, "⏳")
	info, err := waClient.GetGroupInfoFromLink(ctx, m[1])
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil info: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
	sendText(ctx, chat, fmt.Sprintf(
		"👥 *Info Grup*\n\n"+
			"📌 Nama: *%s*\n"+
			"🆔 JID: `%s`\n"+
			"👤 Anggota: %d\n"+
			"📅 Dibuat: %s",
		info.Name,
		info.JID.String(),
		len(info.Participants),
		info.GroupCreated.Format("02 Jan 2006")))
}

// ─── Kode Bahasa ─────────────────────────────────────────────────────────────
// Static list — Port dari tools-kodebahasa.js
// Cmd: !kodebahasa, !kodebhs

func handleKodeBahasa(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	list := `📋 *Daftar Kode Bahasa* (untuk !tr)

` + "```" + `
id  → Indonesian (Indonesia)
en  → English
ms  → Malay
zh  → Chinese
ja  → Japanese
ko  → Korean
ar  → Arabic
fr  → French
de  → German
es  → Spanish
it  → Italian
pt  → Portuguese
ru  → Russian
hi  → Hindi
th  → Thai
vi  → Vietnamese
nl  → Dutch
pl  → Polish
tr  → Turkish
sv  → Swedish
da  → Danish
fi  → Finnish
no  → Norwegian
uk  → Ukrainian
cs  → Czech
ro  → Romanian
hu  → Hungarian
el  → Greek
he  → Hebrew
fa  → Persian
` + "```" + `
Contoh: ` + "`" + `!tr en Halo dunia` + "`" + ` → Hello world`
	sendText(ctx, chat, list)
}

// ─── HD Video (theresav) ──────────────────────────────────────────────────────
// API: theresav winkvideo — Port dari hdvid2.js
// Cmd: !hdvid, !hdvideo

// handleHDVid — !hdvid: upscale video 2× + sharpen jadi HD, PURE FFMPEG
// (tanpa API key — dulu pakai api.theresav.biz.id yang butuh TheresavAPIKey).
// Port dari Ourin plugins/tools/hdvid.js:
//
//	scale=iw*2:ih*2:flags=lanczos   → upscale 2×
//	unsharp=5:5:1.0:5:5:0.0         → sharpening
//	-c:v libx264 -preset fast -crf 23 -c:a copy
func handleHDVid(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetVideoMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf(
			"📹 *HD Video Enhancer*\n\n"+
				"> Perjelas video — kecil di-upscale ke 1080p, besar di-sharpen\n\n"+
				"*Cara pakai:*\n"+
				"> Reply video + ketik `%shdvid`\n\n"+
				"*Catatan:* maksimal 50MB, proses ±30 detik - 2 menit tergantung ukuran.",
			Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	vidData, err := waClient.Download(ctx, quoted.GetVideoMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download video: "+err.Error())
		return
	}
	if len(vidData) > 50<<20 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Video terlalu besar — maksimal 50MB.")
		return
	}

	sendText(ctx, chat, "🎞️ *HD Video Enhancer*\n\nVideo sedang diproses...")

	uid := uuid.New().String()
	_ = os.MkdirAll("temp", 0o755)
	inPath := filepath.Join("temp", "hdvid_in_"+uid+".mp4")
	outPath := filepath.Join("temp", "hdvid_out_"+uid+".mp4")
	if err := os.WriteFile(inPath, vidData, 0o644); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal tulis file sementara.")
		return
	}
	defer os.Remove(inPath)
	defer os.Remove(outPath)

	// Mode pintar: di server 1 vCPU upscale 2× penuh (mis. 1080p → 4K) bisa
	// makan >5 menit. Jadi:
	//   - resolusi < 900px (max sisi): upscale ke 1080p, maks 2×, lanczos
	//   - resolusi >= 900px: cukup sharpen (tanpa upscale) — jauh lebih cepat
	w, h := probeVideoSize(inPath)
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	vf := "unsharp=5:5:1.0:5:5:0.0"
	mode := "sharpened"
	if maxDim > 0 && maxDim < 900 {
		f := math.Min(2.0, math.Min(1920/float64(w), 1080/float64(h)))
		if f > 1.0 {
			vf = fmt.Sprintf("scale=trunc(iw*%f/2)*2:trunc(ih*%f/2)*2:flags=lanczos,unsharp=5:5:1.0:5:5:0.0", f, f)
			mode = "upscaled"
		}
	}

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inPath,
		"-vf", vf,
		"-c:v", "libx264",
		// ultrafast + crf 26: jauh lebih ringan daripada preset fast + crf 23
		// yang dulu (4K bisa >5 menit di 1 vCPU). crf 26 juga nahan ukuran hasil.
		"-preset", "ultrafast",
		"-crf", "26",
		"-c:a", "copy",
		"-movflags", "+faststart",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := runCmdTimeout(cmd, 180*time.Second); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Proses enhance gagal: "+lastLines(stderr.String(), 3))
		return
	}

	resultData, err := os.ReadFile(outPath)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal baca hasil enhance.")
		return
	}

	// Guard upload: hasil yang kebesaran hampir pasti gagal di-upload ke WA
	// (estimasi upload speed server ~100KB/s × timeout 60s ≈ 6MB). Lebih baik
	// tolak dengan penjelasan daripada "upload to whatsapp failed" di tengah.
	const hdMaxResultMB = 50
	if len(resultData) > hdMaxResultMB<<20 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, fmt.Sprintf(
			"❌ Hasil enhance %.1fMB — melebihi batas %dMB.\n\nCoba video yang lebih pendek atau resolusi lebih kecil.",
			float64(len(resultData))/(1<<20), hdMaxResultMB))
		return
	}

	caption := "✨ HD Video Enhancer — di-sharpen"
	if mode == "upscaled" {
		caption = "✨ HD Video Enhancer — upscale + sharpen"
	}
	if err := sendVideo(ctx, chat, resultData, caption, "video/mp4"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

// ─── Encode teks ──────────────────────────────────────────────────────────────
// Local — Port dari tools-enc.js (simple base64 encode)
// Cmd: !enc <teks>

func handleEnc(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	text := strings.TrimSpace(args)

	// Jika ada quoted teks
	if text == "" {
		ci := msgContextInfo(evt)
		if q := ci.GetQuotedMessage(); q != nil {
			text = q.GetConversation()
			if text == "" {
				text = q.GetExtendedTextMessage().GetText()
			}
		}
	}
	if text == "" {
		sendText(ctx, chat, fmt.Sprintf("❌ Contoh: `%senc teks yang mau di-encode`", Prefix))
		return
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	sendText(ctx, chat, fmt.Sprintf("🔐 *Encoded (Base64):*\n\n```%s```", encoded))
}

// ─── Wanted Poster ────────────────────────────────────────────────────────────
// API: popcat.xyz/wanted — Port dari tools-wanted.js (sebenarnya beda dari blur)
// Cmd: !wanted

func handleWanted(ctx context.Context, evt *events.Message) {
	chat := evt.Info.Chat
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf("❌ Reply foto dulu, lalu ketik `%swanted`", Prefix))
		return
	}
	reactMsg(ctx, evt, "⏳")
	imgData, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download.")
		return
	}
	imgURL, err := uguuUpload(imgData, "image.jpg")
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal upload: "+err.Error())
		return
	}
	wantedURL := "https://api.popcat.xyz/v2/wanted?image=" + url.QueryEscape(imgURL)
	result, err := dlGet(wantedURL, nil)
	if err != nil || len(result) == 0 {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal buat wanted poster.")
		return
	}
	if err := sendImage(ctx, chat, result, "🔫 WANTED!"); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}
