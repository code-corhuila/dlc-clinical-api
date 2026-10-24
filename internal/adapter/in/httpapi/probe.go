package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Probe asks a running API whether it is ready (GET /health/ready); it backs
// the container healthcheck, since the distroless image has no curl.
func Probe(ctx context.Context, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/ready", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: status %d", res.StatusCode)
	}
	return nil
}
