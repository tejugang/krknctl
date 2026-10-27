package backup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	DefaultDownloadTimeout = 5 * time.Minute
	DefaultMaxDownloadSize = 100 << 20 // 100 MB
	defaultMaxRedirects    = 10
)

// defaultTransport is nil in production (uses http.DefaultTransport);
// tests override it to trust httptest TLS certificates.
var defaultTransport http.RoundTripper

// DownloadArchive fetches a backup archive from an HTTPS URL into a temporary
// file. It enforces HTTPS-only, a maximum download size, a request timeout,
// and redirect safety. The caller must call cleanup when the temp file is no
// longer needed.
func DownloadArchive(ctx context.Context, rawURL string, maxSize int64, timeout time.Duration) (_ string, _ func(), retErr error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", nil, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("only HTTPS URLs are allowed")
	}
	if maxSize <= 0 {
		maxSize = DefaultMaxDownloadSize
	}
	if timeout <= 0 {
		timeout = DefaultDownloadTimeout
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: defaultTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= defaultMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-HTTPS URL blocked")
			}
			return nil
		},
	}

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to construct download request: %w", err)
	}

	resp, err := httpClient.Do(dlReq)
	if err != nil {
		return "", nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("remote server returned HTTP %d", resp.StatusCode)
	}

	tempFile, err := os.CreateTemp("", "krkn-restore-url-*.tar.gz")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempName := tempFile.Name()
	removeTemp := func() { os.Remove(tempName) }

	defer func() {
		if retErr != nil {
			tempFile.Close()
			removeTemp()
		}
	}()

	limited := io.LimitReader(resp.Body, maxSize+1)
	written, err := io.Copy(tempFile, limited)
	if err != nil {
		return "", nil, fmt.Errorf("error reading response body: %w", err)
	}
	if written > maxSize {
		return "", nil, fmt.Errorf("remote archive exceeds maximum size (%d bytes)", maxSize)
	}
	if err := tempFile.Close(); err != nil {
		return "", nil, fmt.Errorf("failed to finalize downloaded file: %w", err)
	}

	return tempName, removeTemp, nil
}
