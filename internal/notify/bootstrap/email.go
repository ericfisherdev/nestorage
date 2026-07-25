// Package bootstrap builds the notify bounded context's composition-root-only
// dependency (NSTR-89): NewEmailSender, the domain.EmailSender selector
// cmd/server calls to construct whichever sender config.EmailConfig.Enabled
// selects. Deliberately its own package, not folded into internal/notify/adapter:
// mirrors media/bootstrap's identical "a config-consuming builder belongs in
// a peer package, since the adapter package itself never depends on
// internal/platform/config" rationale (see that package's own doc).
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	notifyadapter "github.com/ericfisherdev/nestorage/internal/notify/adapter"
	notifydomain "github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// NewEmailSender builds the domain.EmailSender this deployment uses
// (NSTR-89): NoopEmailSender when emailCfg.Enabled is false (the default —
// zero AWS dependency, so a fresh install never touches AWS and never fails
// startup — see NoopEmailSender's own doc), or an SESEmailSender when true.
// A true Enabled with an incomplete configuration errors here, at startup,
// rather than surfacing on the first real dispatch attempt —
// config.EmailConfig.Validate already catches a missing FromAddress/
// SESRegion before Load even succeeds, so this is defense in depth, not the
// primary check.
//
// ctx bounds AWS config loading (LoadDefaultConfig may reach out to the
// EC2/ECS instance metadata service to resolve credentials) — callers are
// expected to derive it with a bounded timeout, mirroring
// mediabootstrap.NewPhotoStore's identical ctx contract.
func NewEmailSender(ctx context.Context, emailCfg config.EmailConfig, logger *slog.Logger) (notifydomain.EmailSender, error) {
	if !emailCfg.Enabled {
		return notifyadapter.NewNoopEmailSender(logger), nil
	}

	sender, err := notifyadapter.NewSESEmailSender(ctx, notifyadapter.SESEmailParams{
		Region:          emailCfg.SESRegion,
		FromAddress:     emailCfg.FromAddress,
		AccessKeyID:     emailCfg.SESAccessKeyID,
		SecretAccessKey: emailCfg.SESSecretAccessKey,
	})
	if err != nil {
		return nil, fmt.Errorf("notify/bootstrap: create ses email sender: %w", err)
	}
	return sender, nil
}
