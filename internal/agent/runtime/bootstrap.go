package runtime

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/hongle/hl-panel/internal/agent/controlclient"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
)

func (a *Agent) bootstrapWithRetry(ctx context.Context) error {
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	for {
		err := a.Bootstrap(ctx)
		if err == nil || ctx.Err() != nil || !retryableBootstrap(err) {
			return err
		}
		// Response bodies and secrets are deliberately excluded from retry logs.
		a.logger.Warn("panel connection interrupted; retrying enrollment or recovery")
		if err := waitContext(ctx, backoff.Next()); err != nil {
			return err
		}
	}
}

func retryableBootstrap(err error) bool {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return false
	}
	var status *controlclient.HTTPError
	if errors.As(err, &status) {
		return status.StatusCode == http.StatusRequestTimeout || status.StatusCode == http.StatusTooManyRequests || status.StatusCode >= 500
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
