package util

import (
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// TrustProxyHeaders dikonfigurasi dari Router.New() untuk menentukan
// apakah header X-Forwarded-For / X-Real-IP dipercaya.
var TrustProxyHeaders atomic.Bool

// RealIP mengekstrak IP address asli client dari *http.Request,
// mempertimbangkan reverse proxy headers jika TrustProxyHeaders aktif.
func RealIP(r *http.Request) string {
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	if IsPrivateIP(remoteIP) && TrustProxyHeaders.Load() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" {
					return ip
				}
			}
		}

		if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
			return xrip
		}
	}

	return remoteIP
}

// IsPrivateIP memeriksa apakah IP adalah loopback atau private range.
func IsPrivateIP(ip string) bool {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return false
	}

	if ipAddr.IsLoopback() {
		return true
	}
	if ipAddr.IsPrivate() {
		return true
	}
	return false
}
