package httpx

import (
	"net"
	"net/http"

	"megaapp-back/internal/httpx/legacy"
)

// proxyHeaders are the headers nginx sets on every request it forwards. The client's own values
// are overwritten, so a request that carries any of them came through the proxy; a call made
// from the server straight to the backend port carries none.
var proxyHeaders = []string{"X-Real-IP", "X-Forwarded-For", "True-Client-IP"}

// ServerOnlyMiddleware lets through only calls made from the server itself (curl over SSH).
// Modeled on Flatline's allowlist, with one difference: Flatline trusts only the raw TCP peer
// address, but here nginx runs on the same host, so that address is loopback for every request,
// external ones included. The proxy headers are what tell the two apart. The chi RealIP
// middleware rewrites RemoteAddr from headers a client can send (True-Client-IP), so the address
// check is only meaningful together with the header check: without proxy headers RealIP leaves
// RemoteAddr as the real TCP peer.
func ServerOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isServerLocalRequest(r) {
			legacy.WriteResultError(w, http.StatusForbidden, "route is available from the server only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isServerLocalRequest(r *http.Request) bool {
	for _, header := range proxyHeaders {
		if len(r.Header.Values(header)) > 0 {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
