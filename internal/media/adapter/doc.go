// Package adapter holds the media context's infrastructure adapters:
// PhotoValidator (photo_validation.go, the streaming validate-and-stage
// pipeline), LocalPhotoStore (photo_store_local.go, a domain.PhotoStore
// backed by the local filesystem — MEDIA_ROOT), and PhotoRepository
// (photo_postgres.go, the pgx-backed domain.PhotoRepository). NSTR-35 adds
// an S3-backed PhotoStore alongside LocalPhotoStore in this same package,
// reusing photo_validation.go's shared staging/key-building helpers and
// rerunning the same conformance suite (conformance_test.go) against it.
package adapter
