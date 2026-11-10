// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package diag

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

// PprofMux returns a mux with the standard /debug/pprof/ handlers except /cmdline, which would
// reveal command-line arguments (deployments have passed --api-key on the command line). It is
// served on its own listener (never on the metrics listener, which is often exposed through hostPort).
func PprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidatePprofListen enforces the exposure policy: a non-loopback pprof listener requires
// a bearer token.
func ValidatePprofListen(addr, token string) error {
	if addr == "" || isLoopback(addr) || token != "" {
		return nil
	}
	return fmt.Errorf("refusing to serve pprof on non-loopback address %q without --pprof-auth-token", addr)
}

// guard adds optional bearer-token auth and a request rate limit (profiles are expensive).
func guard(next http.Handler, token string, limiter *rate.Limiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if limiter != nil && !limiter.Allow() {
			http.Error(w, "too many profiling requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartPprof serves pprof on addr. Empty addr disables it.
func StartPprof(addr, token string) error {
	if addr == "" {
		klog.Infoln("pprof disabled")
		return nil
	}
	if err := ValidatePprofListen(addr, token); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           guard(PprofMux(), token, rate.NewLimiter(rate.Every(time.Second), 5)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		klog.Infoln("pprof listening on:", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorln("pprof server failed:", err)
		}
	}()
	return nil
}
