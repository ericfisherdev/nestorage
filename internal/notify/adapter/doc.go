// Package adapter contains the notify context's outbound adapters:
// NotificationRepository, the pgx-backed domain.Enqueuer/domain.Outbox and
// inbox-query implementation (postgres.go), and the HTMX web handlers
// serving the sidebar badge and the /notifications inbox (inbox_web.go).
package adapter
