package checkpoint

import (
	"bytes"
	"io"
)

// readCloser aliases the storage return type so test stubs stay readable.
type readCloser = io.ReadCloser

func newReadCloser(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }
