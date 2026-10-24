package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"vendorproof/internal/httpx"
	"vendorproof/internal/identity"
	"vendorproof/internal/logging"
)

// statusWriter captures the response status for access logs.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := logging.NewRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Int("bytes", sw.bytes),
			slog.Duration("duration", time.Since(start)),
			slog.String("request_id", logging.RequestID(r.Context())),
			slog.String("remote", r.RemoteAddr),
		)
	})
}

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logger.ErrorContext(r.Context(), "panic",
					"value", v,
					"request_id", logging.RequestID(r.Context()),
					"stack", string(debug.Stack()),
				)
				httpx.WriteProblem(w, r, http.StatusInternalServerError, "An internal error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func bodyLimit(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func requestTimeout(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate extracts a bearer token, verifies it, and stores only the
// verified issuer/subject. Body fields or arbitrary identity headers are never
// consulted.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || len(header) > 16<<10 {
			unauthorized(w, r, "A bearer access token is required.")
			return
		}
		scheme, token, ok := strings.Cut(header, " ")
		token = strings.TrimSpace(token)
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			unauthorized(w, r, "The Authorization header must use the Bearer scheme.")
			return
		}
		if s.deps.Verifier == nil {
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "Authentication is not configured.")
			return
		}
		ident, err := s.deps.Verifier.Verify(r.Context(), token)
		if err != nil {
			s.deps.Logger.DebugContext(r.Context(), "token rejected", "reason", err)
			unauthorized(w, r, "The access token is missing, invalid, or expired.")
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), ident)))
	})
}

func unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="vendorproof"`)
	httpx.WriteProblem(w, r, http.StatusUnauthorized, detail)
}

func actor(r *http.Request) identity.Identity {
	ident, _ := identityFrom(r.Context())
	return ident
}
