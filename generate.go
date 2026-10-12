// Package gateway marks the module root and owns the code-generation directives.
//
// Run `go generate ./...` from this directory to regenerate Go types from the
// OpenAPI spec (the single source of truth at internal/swagger/openapi.yaml).
package gateway

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config oapi-codegen.yaml internal/swagger/openapi.yaml
