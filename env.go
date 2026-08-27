package main

// env.go — dukungan file .env buat binary hasil compile (project dipublikasi).
//
// Prinsip: SETTING DI config.go TIDAK DIUBAH. Nilai const di sana tetap jadi
// default/fallback. File .env cuma me-load override untuk hal-hal yang wajar
// diganti per-deployment: nomor owner/creator/bot, metode koneksi (pairing/qr),
// dll. Env asli (dari shell/systemd/docker) selalu menang atas .env.
//
// Format .env (baris sederhana):
//
//	# komentar
//	OWNER_NUMBER=628xxx
//	LOGIN_MODE=pairing   # atau qr
//	PAIRING_NUMBER=628xxx

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

var (
	envOnce   sync.Once
	envLoaded bool
)

// loadDotEnv baca .env di folder kerja & set ke environment proses.
// Aman dipanggil berkali-kali (sekali load saja via sync.Once).
func loadDotEnv() {
	if envLoaded {
		return
	}
	envLoaded = true
	f, err := os.Open(".env")
	if err != nil {
		return // tidak ada .env → pakai default dari config.go
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// buang komentar di belakang nilai (hanya kalau ada spasi sebelum #)
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		// buang kutip pembungkus
		if len(val) >= 2 &&
			((val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if key == "" || val == "" {
			continue
		}
		// env asli (shell/systemd) menang — .env hanya pengisi awal.
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

func envStr(key string) string {
	envOnce.Do(loadDotEnv)
	return os.Getenv(key)
}

// ─── Override config via env (.env) ──────────────────────────────────────────
// Kosong = tidak dioverride → pakai const config.go apa adanya.

// envOwnerNumber — OWNER_NUMBER: nomor yang bot anggap owner.
func envOwnerNumber() string { return normalizeNumberEnv(envStr("OWNER_NUMBER")) }

// envCreatorNumber — CREATOR_NUMBER: nomor creator (shell/addowner).
func envCreatorNumber() string { return normalizeNumberEnv(envStr("CREATOR_NUMBER")) }

// envBotNumber — BOT_NUMBER: nomor bot (info & default pairing).
func envBotNumber() string { return normalizeNumberEnv(envStr("BOT_NUMBER")) }

// cfgLoginMode — LOGIN_MODE: "pairing" | "qr" | "" (pakai perilaku default).
func cfgLoginMode() string {
	switch strings.ToLower(strings.TrimSpace(envStr("LOGIN_MODE"))) {
	case "pairing", "pair", "code":
		return "pairing"
	case "qr":
		return "qr"
	default:
		return ""
	}
}

// cfgPairingNumber — PAIRING_NUMBER: nomor tujuan pairing kalau mode pairing.
func cfgPairingNumber() string { return normalizeNumberEnv(envStr("PAIRING_NUMBER")) }

// normalizeNumberEnv buang "+", spasi, dan strip dari nomor di env.
func normalizeNumberEnv(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}