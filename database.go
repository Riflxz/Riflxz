package main

import (
	"encoding/json"
	"os"
	"sync"
)

// ─── Owner / Premium DB ───────────────────────────────────────────────────────
// Simpan di JSON supaya gampang diedit manual & compatible sama format Base-Bot-Wa.
// Format: array of nomor telpon string tanpa "@s.whatsapp.net", contoh ["628xxx"].

var dbMu sync.RWMutex

func readNumbersDB(path string) []string {
	dbMu.RLock()
	defer dbMu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		// Fix: jangan swallow diam-diam — file DB korup harus kelihatan di log,
		// bukan cuma jadi daftar kosong yang membingungkan.
		pool.logger.Warn().Err(err).Str("path", path).Msg("database: JSON korup, perlakukan sebagai kosong")
		return []string{}
	}
	return list
}

func saveNumbersDB(path string, list []string) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if err := os.MkdirAll("database", 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func containsNumber(list []string, num string) bool {
	for _, n := range list {
		if n == num {
			return true
		}
	}
	return false
}

func removeNumber(list []string, num string) []string {
	// Fix LB-04: buat slice baru agar tidak corrupt backing array list.
	// Pattern list[:0] berbagi backing array dan merusak list asli saat di-append.
	var out []string
	for _, n := range list {
		if n != num {
			out = append(out, n)
		}
	}
	return out
}

// ─── Role checks ─────────────────────────────────────────────────────────────

// isCreator: nomor yang hardcode di CreatorNumber (config.go). Satu-satunya yang
// bisa pakai eval, shell, addowner.
func isCreator(number string) bool {
	// Override via .env (CREATOR_NUMBER) tanpa mengubah setting default config.go.
	if v := envCreatorNumber(); v != "" {
		return number == v
	}
	return number == CreatorNumber
}

// isOwnerDB: cek apakah nomor ada di owner.json ATAU dia creator.
func isOwnerDB(number string) bool {
	if isCreator(number) {
		return true
	}
	return containsNumber(readNumbersDB(OwnerDBPath), number)
}

// isPremiumDB: cek apakah nomor ada di premium.json, owner.json, atau creator.
func isPremiumDB(number string) bool {
	if isOwnerDB(number) {
		return true
	}
	return containsNumber(readNumbersDB(PremiumDBPath), number)
}

// ─── Blacklist ───────────────────────────────────────────────────────────────

func addBlacklistDB(number string) error {
	db := readNumbersDB(BlacklistPath)
	if containsNumber(db, number) {
		return nil // sudah ada — idempotent
	}
	return saveNumbersDB(BlacklistPath, append(db, number))
}

func removeBlacklistDB(number string) error {
	db := readNumbersDB(BlacklistPath)
	if !containsNumber(db, number) {
		return nil // tidak ada — idempotent
	}
	return saveNumbersDB(BlacklistPath, removeNumber(db, number))
}
