package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestServerOnlyMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       int
	}{
		{name: "direct local call", remoteAddr: "127.0.0.1:5000", want: http.StatusOK},
		{name: "direct local call over ipv6", remoteAddr: "[::1]:5000", want: http.StatusOK},
		{name: "through nginx from the internet", remoteAddr: "127.0.0.1:5000", headers: map[string]string{"X-Real-IP": "203.0.113.5", "X-Forwarded-For": "203.0.113.5"}, want: http.StatusForbidden},
		{name: "through nginx with spoofed loopback client ip", remoteAddr: "127.0.0.1:5000", headers: map[string]string{"X-Real-IP": "203.0.113.5", "True-Client-IP": "127.0.0.1"}, want: http.StatusForbidden},
		{name: "spoofed loopback forwarded for", remoteAddr: "127.0.0.1:5000", headers: map[string]string{"X-Forwarded-For": "127.0.0.1"}, want: http.StatusForbidden},
		{name: "spoofed true client ip alone", remoteAddr: "127.0.0.1:5000", headers: map[string]string{"True-Client-IP": "127.0.0.1"}, want: http.StatusForbidden},
		{name: "empty proxy header", remoteAddr: "127.0.0.1:5000", headers: map[string]string{"X-Real-IP": ""}, want: http.StatusForbidden},
		{name: "direct non-local peer", remoteAddr: "203.0.113.5:5000", want: http.StatusForbidden},
		{name: "unparsable peer address", remoteAddr: "garbage", want: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// RealIP sits in front exactly as in the real router: it rewrites RemoteAddr from headers.
			handler := chimiddleware.RealIP(ServerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))

			request := httptest.NewRequest(http.MethodGet, "/api/debug/ping", nil)
			request.RemoteAddr = tt.remoteAddr
			for name, value := range tt.headers {
				request.Header[http.CanonicalHeaderKey(name)] = []string{value}
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}
