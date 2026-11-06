package provider

import (
	"net/http"
	"testing"
	"time"
)

type transportMarker struct{}

func (*transportMarker) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

func TestCompatibleHTTPClientKeepsSDKTransportBehavior(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	standard := &http.Transport{MaxIdleConns: 42, ResponseHeaderTimeout: time.Second}
	http.DefaultTransport = standard
	client := compatibleHTTPClient()
	copy, ok := client.Transport.(*http.Transport)
	if !ok || copy == standard || copy.MaxIdleConns != 42 || copy.ResponseHeaderTimeout != 10*time.Minute || standard.ResponseHeaderTimeout != time.Second {
		t.Fatalf("标准Transport未按SDK行为复制: %+v", client.Transport)
	}
	wrapper := &transportMarker{}
	http.DefaultTransport = wrapper
	if client := compatibleHTTPClient(); client.Transport != wrapper {
		t.Fatal("已有Transport包装器被替换")
	}
}
