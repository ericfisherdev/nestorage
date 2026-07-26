package api

// This file is package api (white-box), not api_test — mirroring
// storage/adapter's own api_common_internal_test.go exception:
// openAPISpecYAML is unexported, and this test's whole point is validating
// the exact embedded bytes Serve answers with, which is only directly
// reachable from inside this package.

import (
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
)

// TestOpenAPISpec_ParsesAndValidates is NSTR-57's own spec-validity gate: the
// embedded openapi.yaml must parse as a well-formed document, build a V3
// model, and validate clean against the OpenAPI 3.1 meta-schema. This runs
// under `make test` (no database, no network — both libraries are pure Go),
// so a spec typo fails CI the same way a Go compile error would.
func TestOpenAPISpec_ParsesAndValidates(t *testing.T) {
	doc, err := libopenapi.NewDocument(openAPISpecYAML)
	if err != nil {
		t.Fatalf("libopenapi.NewDocument: %v", err)
	}
	if _, err := doc.BuildV3Model(); err != nil {
		t.Fatalf("BuildV3Model: %v", err)
	}

	// A second, fresh Document for the validator: NewValidator builds its
	// own model internally, and every libopenapi-validator example starts
	// from an un-built Document rather than reusing one BuildV3Model already
	// consumed.
	valDoc, err := libopenapi.NewDocument(openAPISpecYAML)
	if err != nil {
		t.Fatalf("libopenapi.NewDocument (validator): %v", err)
	}
	v, errs := validator.NewValidator(valDoc)
	if len(errs) > 0 {
		t.Fatalf("validator.NewValidator: %v", errs)
	}

	valid, validationErrs := v.ValidateDocument()
	if !valid {
		for _, ve := range validationErrs {
			t.Errorf("spec invalid at line %d, col %d: %s", ve.SpecLine, ve.SpecCol, ve.Message)
		}
		t.Fatal("openapi.yaml does not validate against the OpenAPI 3.1 schema")
	}
}
