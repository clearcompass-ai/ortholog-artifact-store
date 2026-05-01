package keyservice

import (
	"encoding/json"
	"io"
)

// jsonStdMarshal is a tiny indirection so the test file can pass any
// value through Go's standard encoding/json without re-importing it
// at the test header level. Trivial wrapper, kept here so the test
// file's import block stays tight.
func jsonStdMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// jsonStdUnmarshalReader mirrors jsonStdMarshal in the decode
// direction — same rationale (keep the test header free of an
// encoding/json import).
func jsonStdUnmarshalReader(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }
