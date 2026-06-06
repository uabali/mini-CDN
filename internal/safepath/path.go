package safepath

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrEmptyPath      = errors.New("empty path")
	ErrTraversal      = errors.New("path traversal is not allowed")
	ErrUnsupportedDir = errors.New("unsupported content directory")
)

var allowedPrefixes = []string{"/images/", "/videos/", "/css/"}

// CleanRequestPath validates a CDN object path and returns a canonical absolute
// URL path such as /images/test.jpg.
func CleanRequestPath(raw string) (string, error) {
	if raw == "" || raw == "/" {
		return "", ErrEmptyPath
	}

	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	if strings.Contains(decoded, "\x00") {
		return "", ErrTraversal
	}

	parts := strings.Split(decoded, "/")
	for _, part := range parts {
		if part == ".." {
			return "", ErrTraversal
		}
	}

	clean := path.Clean("/" + strings.TrimPrefix(decoded, "/"))
	if clean == "/" || strings.HasSuffix(clean, "/") {
		return "", ErrEmptyPath
	}

	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(clean, prefix) {
			return clean, nil
		}
	}
	return "", ErrUnsupportedDir
}

// SafeJoin maps a validated request path to a filesystem path under root.
func SafeJoin(root, cleanRequestPath string) (string, error) {
	rel := strings.TrimPrefix(cleanRequestPath, "/")
	joined := filepath.Join(root, filepath.FromSlash(rel))

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}

	if absJoined != absRoot && !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", ErrTraversal
	}
	return absJoined, nil
}
