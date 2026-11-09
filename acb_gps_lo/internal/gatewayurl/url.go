package gatewayurl

import (
	"errors"
	"net/url"
	"strings"
)

var ErrInvalid = errors.New("invalid API Gateway URL configuration")

const RouteIDMarker = "{route_id}"

// Origin accepts only an HTTP(S) origin. Service paths are configured separately.
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") || u.Opaque != "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalid
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// EndpointPath requires a complete path from the Gateway root, never a path
// to append to a service-specific prefix. Escapes and traversal are rejected.
func EndpointPath(path string) error {
	if path == "" || !strings.HasPrefix(path, "/") || path == "/" ||
		strings.HasSuffix(path, "/") || strings.Contains(path, "//") ||
		strings.Contains(path, "/api/v1/api/v1/") ||
		strings.ContainsAny(path, "?#\\%") || strings.TrimSpace(path) != path {
		return ErrInvalid
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "." || segment == ".." || segment == "" {
			return ErrInvalid
		}
	}
	return nil
}

func RoutePath(path string) error {
	if err := EndpointPath(path); err != nil {
		return err
	}
	if strings.Count(path, RouteIDMarker) != 1 || !strings.HasSuffix(path, "/"+RouteIDMarker) {
		return ErrInvalid
	}
	return nil
}

func Endpoint(origin, path string) (string, error) {
	base, err := Origin(origin)
	if err != nil || EndpointPath(path) != nil || strings.Contains(path, RouteIDMarker) {
		return "", ErrInvalid
	}
	u, _ := url.Parse(base)
	u.Path = path
	return u.String(), nil
}

func Route(origin, path, id string) (string, error) {
	base, err := Origin(origin)
	if err != nil || RoutePath(path) != nil || id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "/?#") {
		return "", ErrInvalid
	}
	u, _ := url.Parse(base)
	u.Path = strings.TrimSuffix(path, RouteIDMarker) + id
	u.RawPath = strings.TrimSuffix(path, RouteIDMarker) + url.PathEscape(id)
	return u.String(), nil
}
