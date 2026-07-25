package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// SESEmailParams configures NewSESEmailSender (NSTR-89). It mirrors
// config.EmailConfig's SES-related fields but is its own type: an adapter
// package depends on configuration only through the composition root
// passing plain values in, never by importing internal/platform/config
// directly (DIP) — mirrors Nestova's identical SESEmailParams.
type SESEmailParams struct {
	// Region is required.
	Region string
	// FromAddress is the verified sending address SendEmail sends from.
	// Required.
	FromAddress string
	// AccessKeyID / SecretAccessKey are optional static credentials; when
	// both are blank, the AWS SDK's default credential chain (environment,
	// shared config/credentials file, EC2/ECS instance role, etc.) supplies
	// credentials instead.
	AccessKeyID     string
	SecretAccessKey string
}

// sesRetryMaxAttempts caps the SDK's own built-in retryer for every SES
// send — a fixed, generous default for a small household deployment's
// email volume, rather than exposing another environment variable nobody
// needs to tune. Dispatcher's own EMAIL_MAX_ATTEMPTS is the separate,
// user-facing retry knob: this constant only bounds the SDK's handling of
// its own transient failures (throttling, 5xx) WITHIN one Send call.
const sesRetryMaxAttempts = 3

// SESEmailSender is a domain.EmailSender backed by Amazon SES v2's
// SendEmail (NSTR-89), mirroring Nestova's own SESEmailSender. It sends a
// "Simple" message (a subject plus separate HTML/text bodies) rather than a
// raw MIME message or a template — neither attachments nor personalization
// tags are needed for a generic notification email.
//
// Unlike Nestova's version, Send here does not distinguish a recipient
// rejection from any other provider failure: this ticket has no bounce
// handling to trigger (no preference downgrade, no owner warning — that is
// Nestova-specific scope this ticket's own plan does not include), so every
// SES error is simply wrapped and returned, and Dispatcher treats it as
// transient — retried with backoff up to EMAIL_MAX_ATTEMPTS.
type SESEmailSender struct {
	client      *sesv2.Client
	fromAddress string
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.EmailSender = (*SESEmailSender)(nil)

// NewSESEmailSender builds an SESEmailSender against params. Like
// bootstrap.NewEmailSender's own doc describes, this does not make a
// startup reachability call: SES has no equivalent of S3's cheap,
// side-effect-free HeadBucket, so a misconfigured Region/FromAddress that
// slips past this constructor's own blank-check would instead surface on
// the first real SendEmail attempt, through the normal
// dispatch-failure/retry path.
func NewSESEmailSender(ctx context.Context, params SESEmailParams) (*SESEmailSender, error) {
	switch {
	case strings.TrimSpace(params.Region) == "":
		return nil, errors.New("notify/adapter: email sender region must not be blank")
	case strings.TrimSpace(params.FromAddress) == "":
		return nil, errors.New("notify/adapter: email sender from address must not be blank")
	}

	optFns := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(params.Region),
		awsconfig.WithRetryMaxAttempts(sesRetryMaxAttempts),
	}
	if params.AccessKeyID != "" && params.SecretAccessKey != "" {
		optFns = append(optFns, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(params.AccessKeyID, params.SecretAccessKey, ""),
		))
	}
	// Otherwise the SDK's default credential chain applies unchanged — see
	// SESEmailParams.AccessKeyID's own doc.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("notify/adapter: load AWS config: %w", err)
	}

	return &SESEmailSender{
		client:      sesv2.NewFromConfig(awsCfg),
		fromAddress: params.FromAddress,
	}, nil
}

// Send sends msg as a Simple email message — subject, HTML body, and text
// body — to msg.To via SendEmail. Any failure is wrapped and returned as a
// plain error (see this type's own doc for why no rejection is
// distinguished); the SDK's own configured retry budget
// (sesRetryMaxAttempts) is already exhausted for the transient classes it
// covers (throttling, 5xx) by the time an error reaches here.
func (s *SESEmailSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	_, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.fromAddress),
		Destination: &types.Destination{
			ToAddresses: []string{msg.To},
		},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: aws.String(msg.Subject)},
				Body: &types.Body{
					Html: &types.Content{Data: aws.String(msg.HTMLBody)},
					Text: &types.Content{Data: aws.String(msg.TextBody)},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("notify/adapter: send email: %w", err)
	}
	return nil
}
