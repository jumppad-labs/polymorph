// Package fetch resolves local or remote paths using go-getter.
// Remote sources (git repos, HTTP URLs, S3, GCS, etc.) are downloaded
// to a temporary directory. Local paths are returned as-is.
package fetch

import (
	"fmt"
	"os"
	"strings"

	getter "github.com/hashicorp/go-getter"
)

// Result holds the resolved local path and whether cleanup is needed.
type Result struct {
	// Path is the local filesystem path to the fetched content.
	Path string
	// cleanup removes the temp directory if content was downloaded remotely.
	cleanup func()
}

// Close removes any temporary files created during fetch.
// Safe to call multiple times or on local results (no-op).
func (r *Result) Close() {
	if r.cleanup != nil {
		r.cleanup()
	}
}

// Path resolves a source string to a local path.
// For local paths, returns the path directly with no cleanup needed.
// For remote sources, downloads to a temp directory.
func Path(src string) (*Result, error) {
	if isLocal(src) {
		return &Result{Path: src}, nil
	}

	dst, err := os.MkdirTemp("", "polymorph-fetch-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	cleanup := func() { os.RemoveAll(dst) }

	if err := getter.GetAny(dst, src); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to fetch %q: %w", src, err)
	}

	return &Result{
		Path:    dst,
		cleanup: cleanup,
	}, nil
}

// isLocal returns true if the source is a local filesystem path.
func isLocal(src string) bool {
	// Explicit scheme prefixes are remote
	for _, prefix := range []string{"http://", "https://", "s3::", "gcs::", "git::", "hg::"} {
		if strings.HasPrefix(src, prefix) {
			return false
		}
	}
	// github.com/... and similar shorthand are remote
	if strings.HasPrefix(src, "github.com/") ||
		strings.HasPrefix(src, "gitlab.com/") ||
		strings.HasPrefix(src, "bitbucket.org/") {
		return false
	}
	// Everything else is local
	return true
}
