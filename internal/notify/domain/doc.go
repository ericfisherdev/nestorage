// Package domain contains the notify context's aggregate, its outbound
// ports, and its typed enums — no I/O, mirroring storage/domain and
// media/domain's identical split from their own app/adapter siblings.
//
// notify is a bounded context shared by three Sprint 6 tickets: NSTR-44
// (in-app delivery, the aggregate/ports/enums), NSTR-45 (the per-user
// notification_preference model in preference.go/preference_repository.go,
// this ticket's own additions), and NSTR-89 (email delivery, additive — see
// notification.go's own doc for the file boundary). Nestova's
// internal/notify is reference only: it is coupled to
// household.HouseholdID/MemberID throughout and is never imported here.
package domain
