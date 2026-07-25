package config

// email.go adds NSTR-89's own configuration: whether the opt-in email
// channel is active, the SES sender it authenticates with, and the
// dispatcher's own polling/retry knobs. nestcore's own config.EmailConfig
// (config/email.go there) is NOT reused here — its NOTIFY_EMAIL_ENABLED /
// SES_FROM_ADDRESS naming and its lack of dispatcher fields (interval,
// batch size, attempt cap) both diverge from this ticket's own env var
// contract (EMAIL_ENABLED, EMAIL_FROM_ADDRESS — see the reconciliation note
// below), so this is a Nestorage-local type instead, mirroring MediaConfig's
// identical "nestcore has nothing that fits" rationale.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// defaultDispatchInterval is EMAIL_DISPATCH_INTERVAL's default: how often
// Dispatcher.Run polls the outbox for due rows.
const defaultDispatchInterval = 30 * time.Second

// defaultDispatchBatchSize is EMAIL_DISPATCH_BATCH_SIZE's default: how many
// due rows a single dispatch tick claims at most.
const defaultDispatchBatchSize = 20

// defaultMaxAttempts is EMAIL_MAX_ATTEMPTS's default: how many send
// attempts a single email row gets before Dispatcher marks it StatusFailed.
const defaultMaxAttempts = 5

// EmailConfig configures the opt-in email notification channel (NSTR-89).
// Enabled gates only the SES provider block (FromAddress, SESRegion, the
// static credential pair) — see Validate's own doc for why the dispatcher's
// own polling/retry fields are validated unconditionally instead. A fresh
// install sets none of this: Enabled defaults false, bootstrap.NewEmailSender
// returns the no-op sender, and the dispatcher still runs (draining
// whatever preference-driven rows NSTR-45 produces) with zero AWS calls.
type EmailConfig struct {
	// Enabled turns the SES-backed sender on. Consulted only by
	// bootstrap.NewEmailSender — the dispatcher goroutine itself is started
	// unconditionally regardless of this flag (see app.Dispatcher's own
	// doc for why gating it would strand NSTR-45-preference-driven rows).
	Enabled bool
	// FromAddress is the verified SES sending address. Required when
	// Enabled.
	FromAddress string
	// DispatchInterval is how often Dispatcher.Run polls the outbox.
	// Always validated: the dispatcher runs regardless of Enabled.
	DispatchInterval time.Duration
	// DispatchBatchSize caps how many due rows a single tick claims.
	// Always validated, for the same reason as DispatchInterval.
	DispatchBatchSize int
	// MaxAttempts caps how many send attempts a row gets before the
	// dispatcher marks it StatusFailed. Always validated, for the same
	// reason as DispatchInterval.
	MaxAttempts int
	// SESRegion is passed to every SES API request. Required when Enabled.
	SESRegion string
	// SESAccessKeyID / SESSecretAccessKey are optional static credentials.
	// When both are blank, the AWS SDK's default credential chain
	// (environment, shared config/credentials file, EC2/ECS instance
	// role, ...) supplies credentials instead — mirrors corecfg.S3Config's
	// identical field pair.
	SESAccessKeyID     string
	SESSecretAccessKey string
}

// LoadEmail reads EmailConfig from EMAIL_ENABLED, EMAIL_FROM_ADDRESS,
// EMAIL_DISPATCH_INTERVAL, EMAIL_DISPATCH_BATCH_SIZE, EMAIL_MAX_ATTEMPTS,
// SES_REGION, SES_ACCESS_KEY_ID, and SES_SECRET_ACCESS_KEY — unprefixed and
// feature-prefixed, matching every other variable in this package (MEDIA_*,
// S3_*), deliberately NOT NESTORAGE_EMAIL_ENABLED (Sprint 6 reconciliation
// R16: NESTORAGE_ is reserved for NESTORAGE_TEST_DATABASE_URL, a test gate,
// not application config). Every field is parsed unconditionally — whether
// each one's own validation applies is Validate's own decision (see that
// method's doc), mirroring LoadMedia's identical always-parse-caller-gates
// composition.
func LoadEmail() (EmailConfig, []error) {
	var errs []error

	enabled, err := corecfg.Bool("EMAIL_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	dispatchInterval, err := corecfg.Duration("EMAIL_DISPATCH_INTERVAL", defaultDispatchInterval)
	if err != nil {
		errs = append(errs, err)
	}
	dispatchBatchSize, err := corecfg.Int32("EMAIL_DISPATCH_BATCH_SIZE", defaultDispatchBatchSize)
	if err != nil {
		errs = append(errs, err)
	}
	maxAttempts, err := corecfg.Int32("EMAIL_MAX_ATTEMPTS", defaultMaxAttempts)
	if err != nil {
		errs = append(errs, err)
	}

	return EmailConfig{
		Enabled:            enabled,
		FromAddress:        strings.TrimSpace(corecfg.String("EMAIL_FROM_ADDRESS", "")),
		DispatchInterval:   dispatchInterval,
		DispatchBatchSize:  int(dispatchBatchSize),
		MaxAttempts:        int(maxAttempts),
		SESRegion:          strings.TrimSpace(corecfg.String("SES_REGION", "")),
		SESAccessKeyID:     strings.TrimSpace(corecfg.String("SES_ACCESS_KEY_ID", "")),
		SESSecretAccessKey: strings.TrimSpace(corecfg.String("SES_SECRET_ACCESS_KEY", "")),
	}, errs
}

// Validate returns every EmailConfig problem found. DispatchInterval/
// DispatchBatchSize/MaxAttempts are checked unconditionally — Dispatcher.Run
// starts regardless of Enabled (see EmailConfig.Enabled's own doc), so a
// disabled install still needs these three to be sane. FromAddress,
// SESRegion, and the credential pair are checked only when Enabled is true:
// the same "provider block gated by the caller's own feature flag" pattern
// MediaConfig.Validate uses for S3, applied here to SES instead of a
// storage-backend selector.
func (e EmailConfig) Validate() []error {
	var errs []error
	if e.DispatchInterval <= 0 {
		errs = append(errs, fmt.Errorf("EMAIL_DISPATCH_INTERVAL must be positive, got %v", e.DispatchInterval))
	}
	if e.DispatchBatchSize <= 0 {
		errs = append(errs, fmt.Errorf("EMAIL_DISPATCH_BATCH_SIZE must be positive, got %d", e.DispatchBatchSize))
	}
	if e.MaxAttempts <= 0 {
		errs = append(errs, fmt.Errorf("EMAIL_MAX_ATTEMPTS must be positive, got %d", e.MaxAttempts))
	}

	if !e.Enabled {
		return errs
	}

	if e.FromAddress == "" {
		errs = append(errs, errors.New("EMAIL_FROM_ADDRESS is required when EMAIL_ENABLED=true"))
	}
	if e.SESRegion == "" {
		errs = append(errs, errors.New("SES_REGION is required when EMAIL_ENABLED=true"))
	}
	if (e.SESAccessKeyID == "") != (e.SESSecretAccessKey == "") {
		errs = append(errs, errors.New("SES_ACCESS_KEY_ID and SES_SECRET_ACCESS_KEY must be set together (or both left unset to use the default AWS credential chain)"))
	}
	return errs
}
