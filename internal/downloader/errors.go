package downloader

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

// HTTPStatusError is a non-2xx/3xx HTTP response.
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string { return "http " + strconv.Itoa(e.Status) }

// classifyError maps a page failure to a short kind the UI can explain in
// plain language ("dns" -> "server not found"), plus the HTTP status.
func classifyError(err error) (kind string, status int) {
	if err == nil {
		return "", 0
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return "http", statusErr.Status
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns", 0
	}
	if errors.Is(err, ErrFileTooLarge) {
		return "too_large", 0
	}
	if errors.Is(err, ErrNoBrowser) {
		return "browser", 0
	}
	if errors.Is(err, ErrIdleTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout", 0
	}
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &invalidErr) {
		return "tls", 0
	}
	var lw *localWriteError
	if errors.As(err, &lw) {
		return "write", 0
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"):
		return "dns", 0
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused"):
		return "refused", 0
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out"):
		return "timeout", 0
	case strings.Contains(msg, "x509") || strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate"):
		return "tls", 0
	case strings.Contains(msg, "net::err_name_not_resolved"):
		return "dns", 0
	case strings.Contains(msg, "net::err_connection_refused"):
		return "refused", 0
	case strings.Contains(msg, "net::err_cert"):
		return "tls", 0
	case strings.Contains(msg, "net::err_timed_out"):
		return "timeout", 0
	}
	return "other", 0
}
