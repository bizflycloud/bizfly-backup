package storage_vault

import (
	"net"
	"net/http"
	"time"
)

// TransportOptions collects various options which can be set for an HTTP based transport.
type TransportOptions struct {
	Connect          time.Duration
	ConnKeepAlive    time.Duration
	ExpectContinue   time.Duration
	IdleConn         time.Duration
	MaxAllIdleConns  int
	MaxHostIdleConns int
	ResponseHeader   time.Duration
	TLSHandshake     time.Duration
}

// Transport returns a new http.RoundTripper with default settings applied.
func Transport(opts TransportOptions) (http.RoundTripper, error) {
	tr := &http.Transport{
		ResponseHeaderTimeout: opts.ResponseHeader,
		Proxy:                 http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			KeepAlive: opts.ConnKeepAlive,
			DualStack: true,
			Timeout:   opts.Connect,
		}).DialContext,
		MaxIdleConns:          opts.MaxAllIdleConns,
		IdleConnTimeout:       opts.IdleConn,
		TLSHandshakeTimeout:   opts.TLSHandshake,
		MaxIdleConnsPerHost:   opts.MaxHostIdleConns,
		ExpectContinueTimeout: opts.ExpectContinue,
	}

	return RoundTripper(tr), nil
}

func RoundTripper(upstream http.RoundTripper) http.RoundTripper {
	return upstream
}
