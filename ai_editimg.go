package main

// ai_editimg.go — Edit gambar via AI.
// Backend: live3d.io (nano_banana_2). Endpoint publik yang di-reverse-engineer
// dari web live3d.io — butuh header signing AES/RSA.
//
// Alur: upload gambar → buat task → poll status → ambil hasil dari temp.live3d.io.
// Cmd: !editimg <prompt> (reply gambar), !imgedit, !nanoedit

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// live3dConfig — kredensial & konstanta backend live3d.
const (
	live3dPKey = "LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0KTUlHZk1BMEdDU3FHU0liM0RRRUJBUVVBQTRHTkFEQ0JpUUtCZ1FDd2xPK2JvQzZjd1JvM1VmWFZCYWRhWXdjWDB6S1MyZnVWTlkycVowZGd3YjFOSisvUTlGZUFvc0w0T05pb3NENzFvbjNQVllxUlVsTDUwNDVtdkgySzlpOGJBRlZNRWlwN0U2Uk1LNnRLQUFpZjd4elpyWG5QMUdaNVJpanRxZGd3aCtZbXpUbzM5Y3VCQ3NacUs5b0VvZVEzci9teUc5Uys5Y1I1aHVUdUZRSURBUUFCCi0tLS0tRU5EIFBVQkxJQyBLRVktLS0tLQ=="
	live3dAID   = "aifaceswap"
	live3dUID   = "1H5tRtzsBkqXcaJ"
	live3dOrigin = "8f3f0c7387123ae0"
	live3dTheme  = "83EmcUoQTUv50LhNx0VrdcK8rcGexcP35FcZDcpgWsAXEyO4xqL5shCY6sFIWB2Q"
	live3dModel  = "nano_banana_2"
)

// live3dAES — AES-128-CBC, key & IV sama (pola source JS).
func live3dAES(data, key string) string {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return ""
	}
	// PKCS7 padding
	padLen := aes.BlockSize - len(data)%aes.BlockSize
	padded := append([]byte(data), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(key)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out)
}

// live3dRSA — RSA public encrypt PKCS1 (pkey base64 → PEM).
func live3dRSA(data string) string {
	pemBytes, err := base64.StdEncoding.DecodeString(live3dPKey)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return ""
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return ""
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return ""
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, []byte(data))
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(enc)
}

// live3dHeaders — bangun header signing untuk request.
func live3dHeaders(fp, path string) map[string]string {
	i := randomHex(8)
	d := randomUUID()
	n := time.Now().Unix()
	s := live3dRSA(i)

	signStr := fmt.Sprintf("%s:%s:%d:%s:%s", live3dAID, live3dUID, n, d, s)
	if strings.Contains(path, "upload-img") {
		signStr = fmt.Sprintf("%s:%s:%s", live3dAID, d, s)
	}
	return map[string]string{
		"fp":      fp,
		"fp1":     live3dAES(fmt.Sprintf("%s:%s", live3dAID, fp), i),
		"x-guide": s,
		"x-sign":  live3dAES(signStr, i),
		"x-code":  fmt.Sprintf("%d", time.Now().UnixMilli()),
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func randomUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// live3dPost — POST JSON ke live3d dengan header signing.
func live3dPost(fp, path string, payload interface{}) ([]byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://app-v1.live3d.io"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 16; NX729J) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.7499.34 Mobile Safari/537.36")
	req.Header.Set("Origin", "https://live3d.io")
	req.Header.Set("Referer", "https://live3d.io/")
	req.Header.Set("Theme-Version", live3dTheme)
	for k, v := range live3dHeaders(fp, path) {
		req.Header.Set(k, v)
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	return readAllLimit(resp.Body, 50<<20)
}

// live3dUpload — upload gambar via multipart.
func live3dUpload(fp string, img []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "input.jpg")
	if err != nil {
		return "", err
	}
	fw.Write(img)
	w.WriteField("fn_name", "demo-image-editor")
	w.WriteField("request_from", "9")
	w.WriteField("origin_from", live3dOrigin)
	w.Close()

	req, err := http.NewRequest("POST", "https://app-v1.live3d.io/aitools/upload-img", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 16; NX729J) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.7499.34 Mobile Safari/537.36")
	req.Header.Set("Origin", "https://live3d.io")
	req.Header.Set("Referer", "https://live3d.io/")
	req.Header.Set("Theme-Version", live3dTheme)
	for k, v := range live3dHeaders(fp, "/aitools/upload-img") {
		req.Header.Set(k, v)
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	body, err := readAllLimit(resp.Body, 50<<20)
	if err != nil {
		return "", err
	}
	var res struct {
		Data struct {
			Path string `json:"path"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &res) != nil || res.Data.Path == "" {
		return "", fmt.Errorf("upload: path kosong")
	}
	return res.Data.Path, nil
}

// live3dEdit — alur lengkap: upload → create task → poll → return URL hasil.
func live3dEdit(img []byte, prompt string) (string, error) {
	fp := randomHex(16)

	// 1. Upload
	path, err := live3dUpload(fp, img)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}

	// 2. Create task
	createPayload := map[string]interface{}{
		"fn_name":      "demo-image-editor",
		"call_type":    3,
		"input": map[string]interface{}{
			"model":         live3dModel,
			"source_images": []string{path},
			"prompt":        prompt,
			"aspect_radio":  "auto",
			"request_from":  9,
		},
		"data":         "",
		"request_from": 9,
		"origin_from":  live3dOrigin,
	}
	body, err := live3dPost(fp, "/aitools/of/create", createPayload)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	var createRes struct {
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &createRes) != nil || createRes.Data.TaskID == "" {
		return "", fmt.Errorf("create: task_id kosong")
	}
	taskID := createRes.Data.TaskID

	// 3. Poll status (max ~90 detik)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		statusPayload := map[string]interface{}{
			"task_id":      taskID,
			"fn_name":      "demo-image-editor",
			"call_type":    3,
			"request_from": 9,
			"origin_from":  live3dOrigin,
		}
		sBody, err := live3dPost(fp, "/aitools/of/check-status", statusPayload)
		if err != nil {
			return "", fmt.Errorf("status: %w", err)
		}
		var statusRes struct {
			Data struct {
				Status       int    `json:"status"`
				ResultImage  string `json:"result_image"`
			} `json:"data"`
		}
		if json.Unmarshal(sBody, &statusRes) != nil {
			return "", fmt.Errorf("status: parse gagal")
		}
		switch statusRes.Data.Status {
		case 2:
			if statusRes.Data.ResultImage == "" {
				return "", fmt.Errorf("status: result kosong")
			}
			return "https://temp.live3d.io/" + statusRes.Data.ResultImage, nil
		case 3:
			return "", fmt.Errorf("task gagal")
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("timeout menunggu hasil")
}

// handleEditImg — command handler.
func handleEditImg(ctx context.Context, evt *events.Message, args string) {
	chat := evt.Info.Chat
	prompt := strings.TrimSpace(args)

	// Ambil gambar dari quoted message.
	ci := msgContextInfo(evt)
	quoted := ci.GetQuotedMessage()
	if quoted == nil || quoted.GetImageMessage() == nil {
		sendText(ctx, chat, fmt.Sprintf(
			"🖼️ *Edit Image (AI)*\n\n"+
				"> Edit gambar pakai prompt AI\n\n"+
				"*Format:*\n"+
				"> Reply gambar + ketik `%seditimg <prompt>`\n\n"+
				"*Contoh:*\n"+
				"> `%seditimg jadikan anime style`",
			Prefix, Prefix))
		return
	}
	if prompt == "" {
		sendText(ctx, chat, "❌ Promptnya mana? Contoh: `"+Prefix+"editimg jadikan anime`")
		return
	}
	reactMsg(ctx, evt, "⏳")

	img, err := waClient.Download(ctx, quoted.GetImageMessage())
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal download gambar: "+err.Error())
		return
	}

	resultURL, err := live3dEdit(img, prompt)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal edit gambar: "+err.Error())
		return
	}

	resultImg, err := dlGetSafe(resultURL)
	if err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal ambil hasil: "+err.Error())
		return
	}

	senderName := evt.Info.PushName
	if senderName == "" {
		senderName = senderUser(evt)
	}
	tgl := time.Now().Format("02 January 2006")
	caption := fmt.Sprintf(
		"⌯ EDIT IMAGE\n\n"+
			"▸ Prompt: %s\n\n"+
			"▸ Request by: %s\n\n"+
			"© %s –– %s",
		prompt, senderName, BotName, tgl)

	if err := sendImage(ctx, chat, resultImg, caption); err != nil {
		reactMsg(ctx, evt, "❌")
		sendText(ctx, chat, "❌ Gagal kirim: "+err.Error())
		return
	}
	reactMsg(ctx, evt, "✅")
}

var _ = io.Discard