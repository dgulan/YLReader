package fetcher

import "fmt"

// HTTPStatusError preserves a final 5xx status without embedding target URLs,
// headers or response bodies. The frozen upstream error mapping is unchanged.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("fetcher: upstream status %d", e.StatusCode)
}
func (e *HTTPStatusError) Unwrap() error { return ErrUpstreamError }
