package main

// group_state.go — persist config jaga grup + warn count ke
// database/group_state.json supaya tidak hilang saat bot restart
// (blacklist sudah persist; ini menyusul dengan pola yang sama).
//
// Memory tetap sumber kebenaran saat runtime (guardMu / warnState);
// file hanya snapshot untuk survive restart. Load sekali (lazy, saat
// pertama dipakai), save setiap kali ada mutasi.

import (
	"encoding/json"
	"os"
	"sync"
)

// guardConfig — config jaga satu grup (bentuk persist).
type guardConfig struct {
	Antilink   bool   `json:"antilink"`
	Antitoxic  bool   `json:"antitoxic"`
	Welcome    bool   `json:"welcome"`
	WelcomeMsg string `json:"welcomeMsg,omitempty"`
}

// groupStateFile — isi file group_state.json.
type groupStateFile struct {
	Guards map[string]guardConfig    `json:"guards"`
	Warns  map[string]map[string]int `json:"warns"`
}

const groupStatePath = "database/group_state.json"

var (
	groupStateOnce sync.Once
	groupStateMu   sync.Mutex // serialisasi I/O file
)

// ensureGroupState load state dari file sekali saat pertama dipakai.
func ensureGroupState() {
	groupStateOnce.Do(func() {
		data, err := os.ReadFile(groupStatePath)
		if err != nil {
			return // belum ada file — mulai kosong
		}
		var st groupStateFile
		if err := json.Unmarshal(data, &st); err != nil {
			pool.logger.Warn().Err(err).Str("path", groupStatePath).Msg("group_state: JSON korup, mulai kosong")
			return
		}
		guardMu.Lock()
		for key, cfg := range st.Guards {
			guards[key] = &groupGuard{
				Antilink:   cfg.Antilink,
				Antitoxic:  cfg.Antitoxic,
				Welcome:    cfg.Welcome,
				WelcomeMsg: cfg.WelcomeMsg,
			}
		}
		guardMu.Unlock()
		warnState.Lock()
		warnState.m = st.Warns
		if warnState.m == nil {
			warnState.m = map[string]map[string]int{}
		}
		warnState.Unlock()
	})
}

// saveGroupState tulis snapshot memory ke file. Dipanggil setelah mutasi
// guard/warn; kegagalan hanya di-log, tidak menggagalkan operasi.
func saveGroupState() {
	guardMu.Lock()
	gs := make(map[string]guardConfig, len(guards))
	for key, g := range guards {
		gs[key] = guardConfig{
			Antilink:   g.Antilink,
			Antitoxic:  g.Antitoxic,
			Welcome:    g.Welcome,
			WelcomeMsg: g.WelcomeMsg,
		}
	}
	guardMu.Unlock()

	warnState.Lock()
	ws := make(map[string]map[string]int, len(warnState.m))
	for gk, m := range warnState.m {
		cp := make(map[string]int, len(m))
		for uk, c := range m {
			cp[uk] = c
		}
		ws[gk] = cp
	}
	warnState.Unlock()

	groupStateMu.Lock()
	defer groupStateMu.Unlock()
	if err := os.MkdirAll("database", 0o755); err != nil {
		pool.logger.Warn().Err(err).Msg("group_state: gagal buat folder database")
		return
	}
	data, err := json.MarshalIndent(groupStateFile{Guards: gs, Warns: ws}, "", "  ")
	if err != nil {
		pool.logger.Warn().Err(err).Msg("group_state: gagal encode")
		return
	}
	if err := os.WriteFile(groupStatePath, data, 0o644); err != nil {
		pool.logger.Warn().Err(err).Msg("group_state: gagal simpan")
	}
}