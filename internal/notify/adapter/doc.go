// Package adapter contains the notify context's outbound adapters:
// NotificationRepository, the pgx-backed domain.Enqueuer/domain.Outbox and
// inbox-query implementation (postgres.go), the HTMX web handlers serving
// the sidebar badge and the /notifications inbox (inbox_web.go), and
// NSTR-45's own PreferenceRepository (preference_postgres.go) plus the
// /settings/notifications web handlers (preferences_web.go).
package adapter
