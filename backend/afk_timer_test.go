package backend

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestAFKUnit3PreparedReadyAutonomousExpiry(t *testing.T) {
	for _, phase := range []string{"prepared", "ready"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg, _ := config(t, "restricted")
				cfg.MaximumDuration = 80 * time.Millisecond
				cfg.LeaseDuration = 40 * time.Millisecond
				started := time.Now()
				s, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer closeSession(t, s)
				if phase == "ready" && s.Ready(cfg.Identity) != nil {
					t.Fatal("ready失敗")
				}
				// 実時間の配送遅延でmaximumも過ぎるため、期限順序をbubble時計で固定する。
				time.Sleep(39 * time.Millisecond)
				synctest.Wait()
				select {
				case <-s.authority.Context().Done():
					t.Fatal("lease期限より前に失効")
				default:
				}
				// Snapshotや要求による期限評価を一切呼ばず、独立timerの取消を待つ。
				select {
				case <-s.authority.Context().Done():
				case <-time.After(200 * time.Millisecond):
					t.Fatal("自律期限が進まない")
				}
				if elapsed := time.Since(started); elapsed != cfg.LeaseDuration {
					t.Fatalf("lease期限の自律失効時刻が不正: %v", elapsed)
				}
				if snap := s.Snapshot(); snap.State != "revoking" || snap.Reason != "lease-expired" || snap.Owned != 0 {
					t.Fatalf("未開始状態の期限が不正: %+v", snap)
				}
				if s.Activate(cfg.Identity) == nil || s.Renew(cfg.Identity, 0) == nil {
					t.Fatal("期限後の起動")
				}
			})
		})
	}
}
