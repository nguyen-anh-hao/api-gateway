//go:build tools

// Package tools pins the code-generation tooling so `go run`/`go generate`
// resolve a reproducible version (and `go mod tidy` keeps it in go.sum).
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
