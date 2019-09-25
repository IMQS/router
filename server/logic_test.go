package server

import (
	"net/http"
	"net/url"
	"testing"
)

// These tests do not actually launch a live router. They simply test abstract functionality.

func routeSetFromConfig(t *testing.T, cfg_json string) *routeSet {
	cfg := &Config{}
	err := cfg.LoadString(cfg_json)
	if err != nil {
		t.Fatal(err)
	}
	translator, err := newUrlTranslator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return translator.(*routeSet)
}

func badRouteSetFromConfig(t *testing.T, cfgJSON string, expectErr string) {
	cfg := &Config{}
	err := cfg.LoadString(cfgJSON)
	if err != nil {
		if err.Error() != expectErr {
			t.Fatalf("Expected to fail with '%v', but actually failed with '%v'", expectErr, err)
		}
		return
	}
	_, err = newUrlTranslator(cfg)
	if err != nil {
		if err.Error() != expectErr {
			t.Fatalf("Expected to fail with '%v', but actually failed with '%v'", expectErr, err)
		}
		return
	}
	t.Fatalf("Expected to fail with '%v', but actually succeeded", expectErr)
}

// If you expect the route to be invalid, then set expectOutUrl = ""
func verifyRoute(t *testing.T, rs *routeSet, inUrl string, expectOutUrl string) {
	req := http.Request{}
	req.RequestURI = inUrl
	uri, _ := url.Parse(inUrl)
	newUrl, _, _ := rs.processRoute(uri)
	if newUrl != expectOutUrl {
		t.Errorf("route match failed: '%v' -> '%v' (expected '%v')", inUrl, newUrl, expectOutUrl)
	}
}

func TestRouteMatching(t *testing.T) {
	// Various tests, including giving priority to longer matches
	rs := routeSetFromConfig(t, `{
		"Routes": {
			"/no-trailing-slash(.*)": "http://abc.com/555$1",
			"/abc/long/(.*)": "http://abc.com/long/$1",
			"/abc/(.*)": "http://abc.com/123/$1",
			"/static": "http://abc.com/noise",
			"/(.*)": "http://127.0.0.1/www/$1"
	}}`)

	verifyRoute(t, rs, "/abc/long/777", "http://abc.com/long/777") // /abc/long/ must match before /abc/ or /
	verifyRoute(t, rs, "/static", "http://abc.com/noise")          // route with no regex patterns
	verifyRoute(t, rs, "/abc/xyz/", "http://abc.com/123/xyz/")
	verifyRoute(t, rs, "/abc/xyz", "http://abc.com/123/xyz")
	verifyRoute(t, rs, "/abc/", "http://abc.com/123/")
	verifyRoute(t, rs, "/", "http://127.0.0.1/www/")
	verifyRoute(t, rs, "/1/2/3", "http://127.0.0.1/www/1/2/3")
	verifyRoute(t, rs, "/1/2/3/4/5/6/7/8/9/0/1/2/3/4/5/6/7/8/9/0/1/2/3/4/5/6/7/8/9/0", "http://127.0.0.1/www/1/2/3/4/5/6/7/8/9/0/1/2/3/4/5/6/7/8/9/0/1/2/3/4/5/6/7/8/9/0")
	verifyRoute(t, rs, "/no-trailing-slash666", "http://abc.com/555666")

	// Unmatched routes
	rs = routeSetFromConfig(t, `
	{"Routes": {
		"/abc/(.*)": "https://abc.com/123/$1"
	}}`)

	verifyRoute(t, rs, "/", "")
	verifyRoute(t, rs, "/abc", "")
	verifyRoute(t, rs, "/abc/", "https://abc.com/123/")

	// More than one capture
	rs = routeSetFromConfig(t, `
		{"Routes": {
			"/abc/([^/]*)/(.*)": "http://abc/$2/$1"
	}}`)

	verifyRoute(t, rs, "/abc/a/b", "http://abc/b/a")

	// Make sure that when we have a permissive route such as
	//   "/albjs/extile/(.*)": "http://$1"
	// that we only allow whitelisted hostnames.
	rs = routeSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": {
				"Target": "http://$1",
				"ValidHosts": ["good1", ".\\.maptile.example.com"]
			}
	}}`)

	verifyRoute(t, rs, "/albjs/extile/good1/abc", "http://good1/abc")
	verifyRoute(t, rs, "/albjs/extile/a.maptile.example.com/123.png", "http://a.maptile.example.com/123.png")
	verifyRoute(t, rs, "/albjs/extile/b.maptile.example.com/123.png", "http://b.maptile.example.com/123.png")
	verifyRoute(t, rs, "/albjs/extile/badhost/two", "")
	verifyRoute(t, rs, "/albjs/extile/good1:8080/two", "")                          // extension not allowed
	verifyRoute(t, rs, "/albjs/extile/foobar.good1/two", "http://foobar.good1/two") // prefix is allowed
}

func TestInvalidRoutes(t *testing.T) {
	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": "http://$1"
	}}`, "Route /albjs/extile/(.*) needs to have a list of ValidHosts")

	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": {
				"Target": "http://$1",
				"ValidHosts": []
			}
	}}`, "Route /albjs/extile/(.*) needs to have a list of ValidHosts")

	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": {
				"Target": 123
			}
	}}`, "Replacement URL (/albjs/extile/(.*) -> ) may not be empty")

	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": {
				"Target": "http://$1",
				"ValidHosts": 123
			}
	}}`, "Error decoding route /albjs/extile/(.*): json: cannot unmarshal number into Go struct field ConfigRoute.ValidHosts of type []string")

	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": {}
	}}`, "Replacement URL (/albjs/extile/(.*) -> ) may not be empty")

	badRouteSetFromConfig(t, `{
		"Routes": {
			"/albjs/extile/(.*)": 123
	}}`, "Match /albjs/extile/(.*) has invalid value type. Must be either a string, or an object with 'Target' and 'ValidHosts'")
}
