// Package domain contains the notify context's aggregate, its outbound
// ports, and its typed enums — no I/O, mirroring storage/domain and
// media/domain's identical split from their own app/adapter siblings.
//
// notify is a new bounded context shared by two Sprint 6 tickets: NSTR-44
// (in-app delivery, this package's own aggregate/ports/enums) and NSTR-89
// (email delivery, additive — see notification.go's own doc for the file
// boundary). Nestova's internal/notify is reference only: it is coupled to
// household.HouseholdID/MemberID throughout and is never imported here.
package domain
