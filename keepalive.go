package main

import (
	"context"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow"
)

// keepCallAlive kirim heartbeat tiap 8 detik biar call gak auto-mati. Pakai client
// SENDER yang naruh call ini (bukan selalu bot utama) — heartbeat harus dari akun
// yang bikin call-nya.
func keepCallAlive(wa *whatsmeow.Client, callID string, stop <-chan struct{}) {
	self := wa.Store.GetLID()
	if self.IsEmpty() {
		pool.logger.Warn().Str("call_id", callID).Msg("heartbeat: self LID kosong, heartbeat tidak berjalan")
		return
	}

	shortID := callID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()

	pool.logger.Debug().Str("call_id", shortID).Msg("heartbeat mulai")
	for {
		select {
		case <-stop:
			pool.logger.Debug().Str("call_id", shortID).Msg("heartbeat berhenti")
			return
		case <-ticker.C:
			wrapperID := wa.GenerateMessageID()
			node := signaling.BuildHeartbeat(callID, self, wrapperID)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			err := wa.DangerousInternals().SendNode(ctx, node)
			cancel()
			if err != nil {
				pool.logger.Warn().Err(err).Str("call_id", shortID).Msg("heartbeat error")
			}
		}
	}
}
