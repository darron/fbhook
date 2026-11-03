// Package storage defines the object-store operations required for durable
// acceptance and optimistic state transitions. Versions are opaque tokens.
package storage

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("object not found")
var ErrConflict = errors.New("object version conflict")

type Object struct {
	Body    []byte
	Version string
}
type Page struct {
	Keys   []string
	Cursor string
}
type Store interface {
	Get(context.Context, string) (Object, error)
	Create(context.Context, string, []byte) (string, error)
	Replace(context.Context, string, []byte, string) (string, error)
	List(context.Context, string, string, int) (Page, error)
}
