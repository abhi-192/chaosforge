package util

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// NewRunID returns a lexicographically sortable run id.
func NewRunID() string {
	entropy := ulid.Monotonic(rand.Reader, 0)
	return ulid.MustNew(ulid.Timestamp(time.Now().UTC()), entropy).String()
}
