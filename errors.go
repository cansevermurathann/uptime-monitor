package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
)

// Classify typed errors without exposing URLs, DNS names, or certificate details
// from their Error strings.
func requestErrorKind(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "batch_deadline"
	}
	// url.Error can implement Timeout yet return false when its cause is
	// wrapped again. Inspect the complete chain rather than stopping at it.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if timeout, ok := cause.(net.Error); ok && timeout.Timeout() {
			return "timeout"
		}
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "tls_verification"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var network *net.OpError
	if errors.As(err, &network) {
		return "network"
	}
	return "request"
}
