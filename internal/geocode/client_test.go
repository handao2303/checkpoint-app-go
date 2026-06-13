package geocode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientReverse_ParsesAddressAndCaches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("User-Agent"); got != "checkpoint-test/1.0" {
			t.Errorf("User-Agent = %q, want %q", got, "checkpoint-test/1.0")
		}
		query := r.URL.Query()
		for key, want := range map[string]string{
			"lat": "10.800000",
			"lon": "106.600000",
		} {
			if got := query.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		// Photon GeoJSON reverse response.
		_, _ = w.Write([]byte(`{"features":[{"properties":{` +
			`"district":"Phường Bến Nghé","city":"Quận 1","state":"Thành phố Hồ Chí Minh"}}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:      true,
		BaseURL:      server.URL,
		UserAgent:    "checkpoint-test/1.0",
		RateInterval: time.Nanosecond,
	})

	const wantAddr = "Phường Bến Nghé, Quận 1, Thành phố Hồ Chí Minh"
	for range 2 {
		address, err := client.Reverse(context.Background(), 10.8, 106.6)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if address != wantAddr {
			t.Fatalf("address = %q, want %q", address, wantAddr)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}

	province, err := client.Province(context.Background(), 10.8, 106.6)
	if err != nil || province != "Thành phố Hồ Chí Minh" {
		t.Fatalf("Province() = %q, %v", province, err)
	}
}

func TestClientReverse_Disabled(t *testing.T) {
	address, err := NewClient(Config{}).Reverse(context.Background(), 10.8, 106.6)
	if err != nil || address != "" {
		t.Fatalf("Reverse() = %q, %v; want empty address and nil error", address, err)
	}
}

func TestClientReverse_ProviderFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
		timeout time.Duration
	}{
		{
			name: "non-200",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}),
		},
		{
			name: "malformed JSON",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{`))
			}),
		},
		{
			name: "timeout",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				_, _ = w.Write([]byte(`{"display_name":"too late"}`))
			}),
			timeout: 5 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := NewClient(Config{
				Enabled:      true,
				BaseURL:      server.URL,
				UserAgent:    "checkpoint-test/1.0",
				Timeout:      tt.timeout,
				RateInterval: time.Nanosecond,
			})
			address, err := client.Reverse(context.Background(), 10.8, 106.6)
			if err == nil || address != "" {
				t.Fatalf("Reverse() = %q, %v; want empty address and error", address, err)
			}
		})
	}
}
