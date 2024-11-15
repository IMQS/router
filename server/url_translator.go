package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IMQS/log"
)

type scheme string

const (
	schemeUnknown    scheme = ""
	schemeWS                = "ws"
	schemeHTTP              = "http"
	schemeHTTPS             = "https"
	schemeHTTPBridge        = "httpbridge"
	schemeUDP               = "udp"
)

// A target URL
type target struct {
	baseUrl           string                // The replacement string is appended to this
	useProxy          bool                  // True if we route this via the proxy
	requirePermission string                // If non-empty, then first authorize before continuing
	auth              targetPassThroughAuth // Special authentication rules for this target
}

/*
Usage of targetPassThroughAuth fields:

PureHub:

	token
	tokenExpires

SitePro:

	none

ECS:

	none

CouchDB:

	none
*/
type targetPassThroughAuth struct {
	lock         sync.RWMutex // Guards access to all state except for "config", which is immutable
	config       ConfigPassThroughAuth
	token        string                 // A single token shared by all users of the system. "machine to machine", without any user-specific session.
	tokenExpires time.Time              // Expiry date of 'token'
	tokenMap     map[string]interface{} // Map from username to token. For user-specific sessions with another machine.
	tokenLock    map[string]bool        // If an entry exists in here for a username, then we are busy trying to log that user in.
}

// A route that maps from incoming URL to a target URL
type route struct {
	match      string
	matchRe    *regexp.Regexp // Parsed regular expression of 'match'
	replace    string
	target     *target
	validHosts []*regexp.Regexp // If not empty, then the target hostname must be one of these regexes
}

func parseScheme(targetUrl string) scheme {
	switch {
	case targetUrl[0:3] == "ws:":
		return schemeWS
	case targetUrl[0:4] == "udp:":
		return schemeUDP
	case targetUrl[0:5] == "http:":
		return schemeHTTP
	case targetUrl[0:6] == "https:":
		return schemeHTTPS
	case targetUrl[0:11] == "httpbridge:":
		return schemeHTTPBridge
	}
	return schemeUnknown
}

func (r *route) scheme() scheme {
	return parseScheme(r.target.baseUrl)
}

func (r *route) isHostValid(newURL *url.URL) bool {
	for _, h := range r.validHosts {
		if h.Match([]byte(newURL.Host)) {
			return true
		}
	}
	return false
}

// Router configuration when live.
//
// This implements the fast lookup from URL to target.
// It also performs various sanity checks when initialized.
//
// This type is exposed internally via the urlTranslator interface.
// Although this is the only implementation of that interface, by doing it this way,
// we are encapsulating the functionality of the routeSet from the rest of the program.
type routeSet struct {
	routes []*route

	proxy *url.URL

	/////////////////////////////////////////////////
	// Cached state.
	// The following state is computed from 'routes'.
	prefixHash    map[string]*route  // Keys are everything up to the first open parenthesis character '('
	prefixLengths []int              // Descending list of unique prefix lengths
	targetHash    map[string]*target // Keys are the hostname for each of the target routes setup in config
}

func newTarget() *target {
	t := &target{}
	t.auth.tokenMap = make(map[string]interface{})
	t.auth.tokenLock = make(map[string]bool)
	return t
}

// A urlTranslator is responsible for taking an incoming request and rewriting it for an appropriate backend.
type urlTranslator interface {
	// Rewrite an incoming request. If newurl is a blank string, then the URL does not match any route.
	processRoute(uri *url.URL) (newurl string, requirePermission string, passThroughAuth *targetPassThroughAuth)
	// Return the URL of a proxy to use for a given request
	getProxy(errLog *log.Logger, host string) (*url.URL, error)
	// Returns all routes
	allRoutes() []*route
}

func (r *routeSet) computeCaches() error {
	allLengths := map[int]bool{}
	r.prefixHash = make(map[string]*route)
	r.targetHash = make(map[string]*target)
	for _, route := range r.routes {
		openParen := strings.Index(route.match, "(")
		key := ""
		if openParen == -1 {
			// route has no regex captures
			key = route.match
		} else {
			key = route.match[:openParen]
		}
		r.prefixHash[key] = route

		parsedUrl, errUrl := url.Parse(route.target.baseUrl)
		if errUrl != nil {
			return fmt.Errorf("Target URL format incorrect %v:%v", route.target.baseUrl, errUrl)
		}
		if parsedUrl.Host != "" {
			r.targetHash[parsedUrl.Host] = route.target
		}

		allLengths[len(key)] = true
		var err error
		route.matchRe, err = regexp.Compile(route.match)
		if err != nil {
			return fmt.Errorf("Failed to compile regex '%v': %v", route.match, err)
		}
	}

	// Produce descending list of unique prefix lengths
	r.prefixLengths = []int{}
	for x, _ := range allLengths {
		r.prefixLengths = append(r.prefixLengths, x)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(r.prefixLengths)))

	return nil
}

func (r *routeSet) processRoute(uri *url.URL) (newurl string, requirePermission string, passThroughAuth *targetPassThroughAuth) {
	route := r.match(uri)
	if route == nil {
		return "", "", nil
	}

	rewritten := route.matchRe.ReplaceAllString(uri.RequestURI(), route.target.baseUrl+route.replace)

	if len(route.validHosts) != 0 {
		newURL, err := url.Parse(rewritten)
		if err != nil {
			return "", "", nil
		}
		if !route.isHostValid(newURL) {
			return "", "", nil
		}
	}

	return rewritten, route.target.requirePermission, &route.target.auth
}

func (r *routeSet) getProxy(errLog *log.Logger, host string) (*url.URL, error) {
	if r.targetHash[host] == nil {
		// We initially thought that this should be an error, because it means that the router is
		// connecting to a host that we haven't explicitly defined inside our 'targets' list in
		// the router config. This was a fine assumption in 2011.
		// However, we now route to many dockerized hostnames (ie a hostname per service),
		// and we also allow the router to fetch arbitrary traffic, for example through the route:
		//   "/albjs/extile/(.*)": "http://$1",
		// So, long story short - this is not an error.
		//errLog.Errorf("Nil target pointer found in hash for host %v", host)
		return nil, nil
	}
	if !r.targetHash[host].useProxy {
		return nil, nil
	}
	return r.proxy, nil
}

func (r *routeSet) allRoutes() []*route {
	return r.routes
}

func (r *routeSet) match(uri *url.URL) *route {
	// Match from longest prefix to shortest
	// Note that we match only on PATH, not on the full URI - so anything behind the question mark is
	// not going to be matched. That's purely a "stupid" performance optimization. If you need to match
	// behind the question mark, then just go ahead and change this code to match on RequestURI() instead
	// of on Path.
	for _, length := range r.prefixLengths {
		if len(uri.Path) >= length {
			if route := r.prefixHash[uri.Path[:length]]; route != nil {
				return route
			}
		}
	}
	return nil
}

// Ensure that httpbridge targets specify the httpbridge backend port number.
func (r *routeSet) verifyHttpBridgeURLs() error {
	for _, route := range r.routes {
		if route.scheme() == schemeHTTPBridge {
			fmt.Printf("HTTPP Bridge\n")
			parsedURL, err := url.Parse(route.target.baseUrl)
			if err != nil {
				return fmt.Errorf(`Invalid replacement URL "%v": %v`, route.target.baseUrl, err)
			}
			port, _ := strconv.Atoi(parsedURL.Host)
			portRT := strconv.Itoa(port)
			if port == 0 || parsedURL.Host != portRT {
				return fmt.Errorf(`httpbridge target must specify a port number only. The "%v" portion of "%v" is invalid.`, parsedURL.Host, route.target.baseUrl)
			}
		}
	}
	return nil
}

func parseValidHosts(r *ConfigRoute) ([]*regexp.Regexp, error) {
	res := []*regexp.Regexp{}
	for _, v := range r.ValidHosts {
		if len(v) == 0 {
			return nil, fmt.Errorf("ValidHosts entry may not be an empty string")
		}
		// Make sure the regex ends with a terminator. If we don't do this, then it's trivial to
		// extend a valid hostname, even with a port. For example, the hostname "maps" could be
		// changed to "maps:8080", which might be a vulnerable port. Or, "maps" could be extended
		// to "maps.google.com".
		// I can't think of a case where prefix extension would be bad.
		if v[len(v)-1] != '$' {
			v += "$"
		}
		re, err := regexp.Compile(v)
		if err != nil {
			return nil, fmt.Errorf("Failed to compile regex '%v': %v", v, err)
		}
		res = append(res, re)
	}
	return res, nil
}

// Turn a configuration into a runnable urlTranslator
func newUrlTranslator(config *Config) (urlTranslator, error) {
	rs := &routeSet{}

	err := config.verify()
	if err != nil {
		return nil, err
	}

	if config.Proxy != "" {
		rs.proxy, _ = url.Parse(config.Proxy) // config.verify() ensures that the proxy is a legal URL
	}

	targets := map[string]*target{}
	for name, ctarget := range config.Targets {
		t := newTarget()
		t.baseUrl = ctarget.URL
		t.useProxy = ctarget.UseProxy
		t.requirePermission = ctarget.RequirePermission
		t.auth.config = ctarget.PassThroughAuth
		targets[name] = t
	}

	for match, replaceAny := range config.Routes {
		// replace must be either a string or a ConfigRoute
		configRoute := ConfigRoute{}
		if str, ok := replaceAny.(string); ok {
			// Right side is a string. This is simple
			configRoute.Target = str
		} else if any, ok := replaceAny.(map[string]interface{}); ok {
			// And here we do a little hack, serializing back to JSON, and then
			// from that JSON, we go to ConfigRoute.
			str, _ := json.Marshal(any)
			if err := json.Unmarshal(str, &configRoute); err != nil {
				return nil, fmt.Errorf("Error decoding route %v: %v", match, err)
			}
		}
		route := &route{}
		route.match = match
		if len(configRoute.ValidHosts) != 0 {
			var err error
			route.validHosts, err = parseValidHosts(&configRoute)
			if err != nil {
				return nil, fmt.Errorf("In route for '%v': %v", match, err)
			}
		}
		namedTarget, namedSuffix := splitNamedTarget(configRoute.Target)
		if len(namedTarget) != 0 {
			// Named target, which comes from the "Targets" section of the config file
			if targets[namedTarget] == nil {
				return nil, fmt.Errorf("Route target (%v) not defined", namedTarget)
			}
			route.target = targets[namedTarget]
			route.replace = namedSuffix
		} else {
			// An inline target, which is just a string, or (sometimes) a ConfigRoute object
			parsedUrl, errUrl := url.Parse(configRoute.Target)
			if errUrl != nil {
				return nil, fmt.Errorf("Route replacement URL format incorrect %v:%v", configRoute.Target, errUrl)
			}
			route.target = newTarget()
			route.target.useProxy = false
			route.target.baseUrl = parsedUrl.Scheme + "://" + parsedUrl.Host
			route.replace = parsedUrl.Path
			// Assume that the presence of a dollar in the hostname means that the hostname is coming from
			// the src URL. This is a security concern, so we need to make sure that such routes have a whitelist
			// of hostnames that they are allowed to target.
			if strings.Index(parsedUrl.Host, "$") != -1 && len(route.validHosts) == 0 {
				return nil, fmt.Errorf("Route %v needs to have a list of ValidHosts", match)
			}
		}
		// fmt.Printf("Route %v: %v\n", route.match, route.target.baseUrl)
		rs.routes = append(rs.routes, route)
	}

	if err = rs.verifyHttpBridgeURLs(); err != nil {
		return nil, err
	}

	if err = rs.computeCaches(); err != nil {
		return nil, err
	}

	return rs, nil
}
