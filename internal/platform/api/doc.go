// Package api provides the JSON transport plumbing shared by every bounded
// context that mounts a handler under /api/v1: the Router every context
// registers its own routes onto, the one error envelope every endpoint
// answers through, and Observe, the request-observability middleware that
// distinguishes user, integration, and anonymous traffic.
//
// This package imports no bounded context. Anything it needs from one (e.g.
// the resolved principal's kind, for Observe) is injected as a func or a
// narrow interface, so identity, storage, and any future context can all
// depend on this package with no risk of an import cycle.
package api
