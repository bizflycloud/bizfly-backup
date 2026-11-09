package server

import (
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// TestReproHang_ConfigRefreshBlocksOnRunningCronJob tái hiện sự cố treo agent
// ngày 22/07/2026.
//
// handleConfigRefresh gọi <-ctx.Done() để chờ mọi cron job đang chạy kết thúc.
// Job đó là phiên backup (thực tế còn hơn 5 tiếng). Vì paho gọi handler MQTT
// đồng bộ trên goroutine điều phối message, việc chờ này làm hỏng toàn bộ
// MQTT client.
//
// Test PASS  = lỗi vẫn còn (handleConfigRefresh treo).
// Test FAIL  = lỗi đã được vá (handleConfigRefresh trả về ngay).
func TestReproHang_ConfigRefreshBlocksOnRunningCronJob(t *testing.T) {
	s := &Server{
		cronManager: cron.New(cron.WithParser(cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor))),
		mappingToCronEntryID: make(map[string]cron.EntryID),
		logger:               zap.NewNop(),
	}
	s.cronManager.Start()
	defer s.cronManager.Stop()

	// Job đại diện cho phiên backup đang chạy.
	var once sync.Once
	started := make(chan struct{})
	if _, err := s.cronManager.AddFunc("@every 1s", func() {
		once.Do(func() { close(started) })
		time.Sleep(30 * time.Second) // thực tế: ETA 5:20:24
	}); err != nil {
		t.Fatalf("AddFunc: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cron job không khởi chạy")
	}

	// Mô phỏng agent nhận event refresh_config từ MQTT.
	done := make(chan struct{})
	go func() {
		_ = s.handleConfigRefresh(nil)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("handleConfigRefresh trả về ngay -> lỗi đã được vá")
	case <-time.After(3 * time.Second):
		t.Log("TÁI HIỆN THÀNH CÔNG: handleConfigRefresh vẫn treo sau 3s, " +
			"nó sẽ chờ tới khi job backup kết thúc")
	}
}

// TestFix_ConfigRefreshReturnsImmediately kiểm chứng bản vá: bỏ <-ctx.Done()
// thì hàm trả về ngay dù cron job vẫn đang chạy, và job đó vẫn chạy tiếp
// bình thường tới khi hoàn tất.
func TestFix_ConfigRefreshReturnsImmediately(t *testing.T) {
	s := &Server{
		cronManager: cron.New(cron.WithParser(cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor))),
		mappingToCronEntryID: make(map[string]cron.EntryID),
		logger:               zap.NewNop(),
	}
	s.cronManager.Start()
	defer s.cronManager.Stop()

	var once sync.Once
	started := make(chan struct{})
	jobFinished := make(chan struct{})
	if _, err := s.cronManager.AddFunc("@every 1s", func() {
		once.Do(func() {
			close(started)
			time.Sleep(2 * time.Second)
			close(jobFinished)
		})
	}); err != nil {
		t.Fatalf("AddFunc: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cron job không khởi chạy")
	}

	// Bản vá: Stop() không kèm <-ctx.Done().
	refreshed := make(chan struct{})
	go func() {
		s.cronManager.Stop()
		s.cronManager = cron.New()
		s.cronManager.Start()
		s.mappingToCronEntryID = make(map[string]cron.EntryID)
		s.addToCronManager(nil)
		close(refreshed)
	}()

	select {
	case <-refreshed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("bản vá vẫn treo")
	}

	// Job cũ không bị giết, vẫn chạy tới hết.
	select {
	case <-jobFinished:
		t.Log("BẢN VÁ ĐẠT: refresh trả về ngay, backup đang chạy vẫn hoàn tất")
	case <-time.After(5 * time.Second):
		t.Fatal("job đang chạy bị gián đoạn")
	}
}
