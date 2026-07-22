// Package nestorage is the root of the Nestorage household storage service:
// items, the bins that hold them, and the locations those bins live in.
//
// The package itself carries no implementation. It exists so the module has a
// buildable root from the first commit, which is what lets CI run `go build`,
// `go vet` and the linter against a real target rather than an empty tree.
// Bounded contexts land under internal/<context>/{domain,app,adapter}.
package nestorage
