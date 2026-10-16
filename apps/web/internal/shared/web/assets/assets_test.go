package assets

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(Handler())
	t.Cleanup(server.Close)
	return server
}

func TestHandlerServesCSS(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	resp, err := http.Get(server.URL + "/assets/css/output.css")
	if err != nil {
		t.Fatalf("GET css failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", contentType)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("css body should not be empty")
	}
}

func TestHandlerServesJS(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	resp, err := http.Get(server.URL + "/assets/js/htmx.min.js")
	if err != nil {
		t.Fatalf("GET js failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType == "" {
		t.Error("Content-Type should be set for the javascript asset")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "htmx") {
		t.Error("vendored htmx script should be served")
	}
}

func TestHandlerRejectsUnknownPaths(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	for _, path := range []string{"/assets/nope.js", "/assets/css/missing.css", "/not-assets"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestHandlerDoesNotServeDirectoryListing(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	resp, err := http.Get(server.URL + "/assets/")
	if err != nil {
		t.Fatalf("GET assets/ failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "index.html") {
		t.Error("directory listing should not be served")
	}
}
