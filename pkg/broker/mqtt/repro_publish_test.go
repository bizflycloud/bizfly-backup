package mqtt

import (
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

// silentBroker chấp nhận CONNECT, trả CONNACK, sau đó nuốt mọi gói tin và
// không bao giờ trả PUBACK — mô phỏng broker đã chết/treo ở tầng ứng dụng.
func silentBroker(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				// Gói đầu tiên là CONNECT -> trả CONNACK chấp nhận.
				if _, err := c.Read(buf); err != nil {
					return
				}
				if _, err := c.Write([]byte{0x20, 0x02, 0x00, 0x00}); err != nil {
					return
				}
				// Từ đây im lặng tuyệt đối: đọc và vứt, không PUBACK, không PINGRESP.
				for {
					if _, err := c.Read(buf); err != nil {
						if err == io.EOF {
							return
						}
						return
					}
					select {
					case <-done:
						return
					default:
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { close(done); ln.Close() }
}

// TestReproHang_PublishSpinsForever tái hiện Lỗi B: vòng lặp
//
//	for !token.WaitTimeout(tokenWaitTimeout) {}
//
// trong MQTTBroker.Publish vô hiệu hoá timeout 3s và chờ vĩnh viễn khi broker
// không trả PUBACK.
//
// Test PASS = lỗi vẫn còn (Publish không bao giờ trả về).
// Test FAIL = lỗi đã được vá (Publish trả về lỗi timeout).
func TestReproHang_PublishSpinsForever(t *testing.T) {
	addr, stop := silentBroker(t)
	defer stop()

	b, err := NewBroker(
		WithURL("mqtt://"+addr),
		WithClientID("repro-agent"),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	returned := make(chan error, 1)
	go func() { returned <- b.Publish("agent/repro", []byte(`{"percent":"28.86%"}`)) }()

	// tokenWaitTimeout = 3s. Nếu timeout được tôn trọng, Publish phải trả về
	// trong khoảng đó. Ta chờ gấp 5 lần cho chắc.
	select {
	case err := <-returned:
		t.Fatalf("Publish đã trả về (%v) -> lỗi đã được vá", err)
	case <-time.After(15 * time.Second):
		t.Log("TÁI HIỆN THÀNH CÔNG: Publish vẫn chưa trả về sau 15s " +
			"dù tokenWaitTimeout chỉ là 3s -> chờ vô hạn")
	}
}
