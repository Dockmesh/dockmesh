package openapi_test

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/openapi"
)

// The bundler keeps duplicate mapping keys; the JSON served at
// /api/v1/openapi.json refuses them, so the drift test alone stays green.
func TestJSONBytesParses(t *testing.T) {
	if _, err := openapi.JSONBytes(); err != nil {
		t.Fatalf("openapi.JSONBytes: %v", err)
	}
}
