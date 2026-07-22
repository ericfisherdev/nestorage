// Package domain holds the identity bounded context's domain model: the User
// aggregate, typed ids, the Role and UserColor enums, sentinel errors, the
// UserRepository port, and the ValidatePassword/NormalizeEmail rules every
// write path shares. Persistence adapters live in the sibling adapter
// package.
package domain
