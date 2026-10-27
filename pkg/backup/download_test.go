package backup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDownloadArchiveRejectsHTTP(t *testing.T) {
	_, _, err := DownloadArchive(context.Background(), "http://example.com/backup.tar.gz", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS error, got: %v", err)
	}
}

func TestDownloadArchiveRejectsInvalidURL(t *testing.T) {
	_, _, err := DownloadArchive(context.Background(), "not-a-url", 0, 0)
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestDownloadArchiveRejectsEmptyURL(t *testing.T) {
	_, _, err := DownloadArchive(context.Background(), "", 0, 0)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestDownloadArchiveSuccess(t *testing.T) {
	payload := []byte("fake-archive-data-1234567890")
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(payload)
	}))
	defer ts.Close()

	oldTransport := defaultTransport
	defaultTransport = ts.Client().Transport
	t.Cleanup(func() { defaultTransport = oldTransport })

	path, cleanup, err := DownloadArchive(context.Background(), ts.URL+"/backup.tar.gz", 0, 10*time.Second)
	if err != nil {
		t.Fatalf("DownloadArchive() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup function is nil on success")
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("downloaded content = %q, want %q", data, payload)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the temporary file")
	}
}

func TestDownloadArchiveRejectsOversizedResponse(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1024))
	}))
	defer ts.Close()

	oldTransport := defaultTransport
	defaultTransport = ts.Client().Transport
	t.Cleanup(func() { defaultTransport = oldTransport })

	_, _, err := DownloadArchive(context.Background(), ts.URL+"/big.tar.gz", 512, 10*time.Second)
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("expected size error, got: %v", err)
	}
}

func TestDownloadArchiveRejectsNon200(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	oldTransport := defaultTransport
	defaultTransport = ts.Client().Transport
	t.Cleanup(func() { defaultTransport = oldTransport })

	_, _, err := DownloadArchive(context.Background(), ts.URL+"/missing.tar.gz", 0, 10*time.Second)
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected HTTP 404 error, got: %v", err)
	}
}

func TestDownloadArchiveCleansUpOnError(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2048))
	}))
	defer ts.Close()

	oldTransport := defaultTransport
	defaultTransport = ts.Client().Transport
	t.Cleanup(func() { defaultTransport = oldTransport })

	_, cleanup, err := DownloadArchive(context.Background(), ts.URL+"/big.tar.gz", 1024, 10*time.Second)
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}
