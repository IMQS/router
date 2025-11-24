package server

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	golog "log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	// "github.com/cespare/hutil/apachelog" // Newer, but doesn't support websockets
	apachelog "github.com/IMQS/go-apachelog" // Older, but supports websockets. Forked to include time zone in access logs.
	"github.com/IMQS/log"
	"github.com/IMQS/serviceauth"
	serviceconfig "github.com/IMQS/serviceconfigsgo"

	"golang.org/x/net/http2"
	"golang.org/x/net/websocket"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Router Server
type Server struct {
	httpTransport *http.Transport // For talking to backend services
	configHttp    ConfigHTTP
	accessLogFile string
	debugRoutes   bool // If enabled, dumps every translated route to the error log
	translator    urlTranslator
	errorLog      *log.Logger
	wsdlMatch     *regexp.Regexp // hack for serving static content
	udpConnPool   *UDPConnectionPool
}

type frontServer struct {
	isSecure bool
	server   *Server
}

func (f *frontServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.server.ServeHTTP(f.isSecure, w, req)
}

// NewServer creates a new server instance; starting up logging and creating a routing instance.
func NewServer(config *Config) (*Server, error) {
	var err error
	s := &Server{}
	s.configHttp = config.HTTP
	s.udpConnPool = NewUDPConnectionPool()

	s.debugRoutes = config.DebugRoutes
	s.accessLogFile = config.AccessLog
	s.errorLog = log.New(pickLogfile(config.ErrorLog), false)
	if config.LogLevel != "" {
		if lev, err := log.ParseLevel(config.LogLevel); err != nil {
			s.errorLog.Errorf("%v", err)
		} else {
			s.errorLog.Level = lev
		}
	}

	if s.translator, err = newUrlTranslator(config); err != nil {
		return nil, err
	}

	s.httpTransport = &http.Transport{
		DisableKeepAlives:     config.HTTP.DisableKeepAlive,
		MaxIdleConnsPerHost:   config.HTTP.MaxIdleConnections,
		DisableCompression:    true,
		ResponseHeaderTimeout: time.Second * time.Duration(config.HTTP.ResponseHeaderTimeout),
	}
	s.httpTransport.Proxy = func(req *http.Request) (*url.URL, error) {
		return s.translator.getProxy(s.errorLog, req.URL.Host)
	}

	// Set both the host and port as system config variables
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	if err := serviceconfig.AddSystemVariableToConfigService("router_http_host", hostname); err != nil {
		return nil, err
	}
	if err := serviceconfig.AddSystemVariableToConfigService("router_http_port", getRouterPort(s.configHttp.Port)); err != nil {
		return nil, err
	}

	s.errorLog.Info("Router starting with:")
	s.errorLog.Infof(" DisableKeepAlives: %v", config.HTTP.DisableKeepAlive)
	s.errorLog.Infof(" MaxIdleConnsPerHost: %v", config.HTTP.MaxIdleConnections)
	s.errorLog.Infof(" ResponseHeaderTimeout: %v", config.HTTP.ResponseHeaderTimeout)
	s.wsdlMatch = regexp.MustCompile(`([^/]\w+)\.(wsdl)$`)
	return s, nil
}

func getRouterPort(port uint16) string {
	if port == 0 {
		return "80"
	}
	return strconv.FormatUint(uint64(port), 10)
}

// Run the server.
// Returns the first error from the first listener that aborts.
func (s *Server) ListenAndServe() error {
	httpAddr := fmt.Sprintf(":%v", s.configHttp.GetPort())
	httpAddrSecondary := ""
	if s.configHttp.SecondaryPort != 0 {
		httpAddrSecondary = fmt.Sprintf(":%v", s.configHttp.SecondaryPort)
	}
	secureAddr := ""
	if s.configHttp.EnableHTTPS {
		secureAddr = ":https"
		if s.configHttp.HTTPSPort != 0 {
			secureAddr = fmt.Sprintf(":%v", s.configHttp.HTTPSPort)
		}
	}

	errors := make(chan error)
	defer close(errors)

	accessLog := openLog(s.accessLogFile, os.Stdout)

	logForwarder := golog.New(log.NewForwarder(0, log.Info, s.errorLog), "", 0)

	runHttp := func(addr string, secure bool, errors chan error) {
		hs := &http.Server{}
		hs.Addr = addr
		hs.Handler = &frontServer{secure, s}
		hs.ErrorLog = logForwarder

		// Newer apachelog (see comments in package includes list)
		//hs.Handler = apachelog.NewHandler(`%h - %u %t "%r" %s %b %T`, s, accessLog)

		// Older apachelog
		hs.Handler = apachelog.NewHandler(hs.Handler, accessLog)

		var err error
		for {
			if secure {
				if serviceconfig.IsContainer() {
					err = fetchCerts(s.configHttp.CertFile, s.configHttp.CertKeyFile)
					if err != nil {
						break
					}
				}
				hs.TLSConfig = &tls.Config{
					MinVersion:               tls.VersionTLS12,
					PreferServerCipherSuites: true,
				}
				err = hs.ListenAndServeTLS(s.configHttp.CertFile, s.configHttp.CertKeyFile)
			} else {
				err = hs.ListenAndServe()
			}
			if !s.autoRestartAfterError(err) {
				break
			}
		}
		errors <- err
	}

	go runHttp(httpAddr, false, errors)
	if httpAddrSecondary != "" {
		go runHttp(httpAddrSecondary, false, errors)
	}
	if secureAddr != "" {
		go runHttp(secureAddr, true, errors)
	}

	// Wait for the first non-nil error and return it
	for err := range errors {
		if err != nil {
			s.errorLog.Errorf(`Router exiting. First non-nil error was "%v"`, err)
			return err
		}
	}

	// unreachable
	return nil
}

// Fetches the certs from the config service and stores them locally
func fetchCerts(certPath, certKeyPath string) error {
	var err error
	//create the directories
	err = os.MkdirAll(filepath.Dir(certPath), os.ModePerm)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(certKeyPath), os.ModePerm)
	if err != nil {
		return err
	}

	//fetch the file contents and store them in the paths supplied
	bytes, err := serviceconfig.GetConfigJson("", serviceName, serviceConfigVersion, filepath.Base(certPath), false)
	if err != nil {
		return err
	}
	err = os.WriteFile(certPath, bytes, os.ModePerm)
	if err != nil {
		return err
	}

	bytes, err = serviceconfig.GetConfigJson("", serviceName, serviceConfigVersion, filepath.Base(certKeyPath), false)
	if err != nil {
		return err
	}
	err = os.WriteFile(certKeyPath, bytes, os.ModePerm)
	if err != nil {
		return err
	}

	return nil
}

func pickLogfile(logfile string) string {
	if logfile != "" {
		return logfile
	}
	return log.Stdout
}

// Certain benign errors seem to occur frequently, and we don't want to shut ourselves down when
// that happens. Instead, we just fire ourselves up again.
func (s *Server) autoRestartAfterError(err error) bool {
	if strings.Contains(err.Error(), "specified network name is no longer available") {
		s.errorLog.Warn("Automatically restarting after receiving error 64")
		return true
	}
	return false
}

// Detect illegal requests
func (s *Server) isLegalRequest(req *http.Request) bool {
	// We were getting a whole lot of requests to the 'telco' server where the hostname was "yahoo.mail.com".
	// TODO: move this to blacklist config file
	if req.URL.Host == "yahoo.mail.com" {
		s.errorLog.Errorf("Illegal hostname (%s) - closing connection", req.URL.Host)
		return false
	}
	return true
}

// ServeHTTP is the single router access point to the frontdoor server. All
// request are handled in this method. It uses Routes to generate the new url
// and then switches on scheme type to connect to the backend copying between
// these pipes.
func (s *Server) ServeHTTP(isSecure bool, w http.ResponseWriter, req *http.Request) {
	// HACK! Doesn't belong here!
	// Catch wsdl here to statically serve.
	filename := s.wsdlMatch.FindString(req.RequestURI)
	if filename != "" {
		http.ServeFile(w, req, "C:\\imqsbin\\conf\\"+filename)
		return
	}

	// Detect malware, DOS, etc
	if !s.isLegalRequest(req) {
		http.Error(w, "", http.StatusTeapot)
		return
	}

	// Redirect HTTP requests to HTTPS
	// Requests from IP addressses and localhost are left untouched
	if s.configHttp.RedirectHTTP && !isSecure && net.ParseIP(req.Host) == nil && req.Host != "localhost" {

		// Give 404 for appcache manifest request when HTTP redirection is enabled, this clears out old manifest
		if req.RequestURI == "/manifest.appcache" {
			http.Error(w, "", http.StatusNotFound)
			s.errorLog.Info("Appcache manifest cleared")
			return
		}

		// Only request to the root of the domain will get redirected, all other requests remain untouched, for instance
		// http://demo.imqs.co.za will get redirected, but http://demo.imqs.co.za/index.html won't
		if req.RequestURI == "/" || req.RequestURI == "" {
			host := strings.Split(req.Host, ":")[0] // remove port from host, this is safe even when no port is specified
			target := fmt.Sprintf("https://%s%s", host, req.URL.Path)
			if s.configHttp.HTTPSPort != 0 {
				target = fmt.Sprintf("https://%s:%d%s", host, s.configHttp.HTTPSPort, req.URL.Path) // override default HTTPS port
			}
			if len(req.URL.RawQuery) > 0 {
				target += "?" + req.URL.RawQuery
			}
			w.Header().Set("cache-control", "no-store")
			s.errorLog.Infof("Redirecting request from %s to %s \n", req.URL.String(), target)
			http.Redirect(w, req, target, http.StatusMovedPermanently)
			return
		}
	}

	// Catch ping requests
	if req.RequestURI == "/router/ping" {
		s.Pong(w, req)
		return
	}

	newurl, requirePermission, passThroughAuth := s.translator.processRoute(req.URL)

	if s.debugRoutes {
		s.errorLog.Infof("(%v) -> (%v)", req.RequestURI, newurl)
	}

	if newurl == "" {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}

	authData, authOK := s.authorize(w, req, requirePermission)
	if !authOK {
		return
	}

	if !authPassThrough(s.errorLog, w, req, authData, passThroughAuth) {
		return
	}

	switch parseScheme(newurl, &req.Header) {
	case schemeHTTPSSE:
		fallthrough
	case schemeHTTPSSSE:
		s.forwardHttpSse(w, req, newurl)
	case schemeHTTP:
		fallthrough
	case schemeHTTPS:
		s.forwardHttp(w, req, newurl)
	case schemeWS:
		s.forwardWebsocket(w, req, newurl)
	case schemeUDP:
		s.forwardUDP(w, req, newurl)
	default:
		s.errorLog.Errorf("Unrecognized scheme (%v) -> (%v)", req.RequestURI, newurl)
		http.Error(w, "Unrecognized forwarding URL", http.StatusInternalServerError)
	}
}

// forwardHttpSse handles connections using server sent events (sse). Since we do not
// use ssl connections from our router to backend services we use what is known as
// h2c which allows for clear text to be sent through a non ssl connection. This is
// then copied back into the connections from the front-end. The reason that we use
// ssl sse from the front-end is two fold:
//  1. The ssl http2 sse event does not gobble up precious fe connections (https sse connections
//     is limited on the front-end to 100 (not the 6 of http 1.1)
//  2. this can be viewed as a lite weight single direction websocket where the only
//     purpose is to keep the user informed of long running transaction (if they so choose)
func (s *Server) forwardHttpSse(w http.ResponseWriter, req *http.Request, newurl string) {
	cleaned, err := http.NewRequest(req.Method, newurl, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy headers from client req into cleaned req, replacing Location header value if found.
	copyheadersIn(req.Header, cleaned.Header)
	cleaned.Proto = req.Proto
	if remoteAddrNoPort, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		cleaned.Header.Add("X-Forwarded-For", remoteAddrNoPort)
	}

	s.addXOriginalPath(req, cleaned)

	settings := []http2.Setting{
		{ID: http2.SettingMaxConcurrentStreams, Val: 10},
		{ID: http2.SettingInitialWindowSize, Val: 65535},
	}
	settingsBuf := &bytes.Buffer{}
	for _, s := range settings {
		binary.Write(settingsBuf, binary.BigEndian, s.ID)
		binary.Write(settingsBuf, binary.BigEndian, s.Val)
	}
	settingsPayload := base64.RawURLEncoding.EncodeToString(settingsBuf.Bytes())

	cleaned.Header.Add("Accept", "text/event-stream")
	cleaned.Header.Set("Connection", "Upgrade, HTTP2-Settings")
	cleaned.Header.Set("Upgrade", "h2c")
	cleaned.Header.Set("HTTP2-Settings", settingsPayload)

	client := &http.Client{
		Timeout: (20 * time.Minute),
	}

	srvResp, err := client.Do(cleaned)
	if err != nil {
		s.errorLog.Info(fmt.Sprintf("Error on client Do %v\n", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer srvResp.Body.Close()

	// Copy headers from response into w, replacing Location header value back to original if found.
	copyHeaders(srvResp.Header, w.Header())
	w.WriteHeader(srvResp.StatusCode)

	buffer := make([]byte, 255)
	for {
		select {
		case <-req.Context().Done():
			// Request closed by client
			return
		default:
			n, err := srvResp.Body.Read(buffer[0:])
			if err != nil {
				if err == io.EOF {
					return
				}
				s.errorLog.Info("Could not read sse body " + err.Error())
				return
			}
			_, err = fmt.Fprintf(w, "%v", string(buffer[:n]))
			if err != nil {
				s.errorLog.Info("Could not write to sse front end " + err.Error())
				return
			}

			// TODO: replace apache logging with slog.
			// This, and the change in IMQS/go-apachelog is required to get to the underlying
			// ResponseWriter to flush the interface,
			if apRec, ok := w.(*apachelog.Record); ok {
				if fl, ok := apRec.ResponseWriter.(http.Flusher); ok {
					fl.Flush()
				} else {
					s.errorLog.Info("Could not get flusher in formwardHTTPSSe")
				}
			}
		}
	}
}

/*
forwardHTTP connects to all http scheme backends and copies bidirectionally between the incoming
connections and the backend connections. It also copies required HTTP headers between the connections making the
router "middle man" invisible to incoming connections.
The body part of both requests and responses are implemented as Readers, thus allowing the body contents
to be copied directly down the sockets, negating the requirement to have a buffer here. This allows all
http bodies, i.e. chunked, to pass through.

On the removal of the "Connection: close" header:
Leaving "Connection: close" is going to instruct the backend to close the HTTP connection
after a single request, which is in conflict with HTTP keep alive. If s.httpTransport.DisableKeepAlives
is false, then we DO want to enable keep alives. It might be better to only remove this header
if s.httpTransport.DisableKeepAlives is true, but it seems prudent to just get rid of it completely.

This issue first became apparent when running the router behind nginx. The backend server behind
router would react to the "Connection: close" header by closing the TCP connection after
the response was sent. This would then result in s.httpTransport.RoundTrip(cleaned) returning
an EOF error when it tried to re-use that TCP connection.
*/
func (s *Server) forwardHttp(w http.ResponseWriter, req *http.Request, newurl string) {
	cleaned, err := http.NewRequest(req.Method, newurl, req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// srcHost := req.Host     // Client address.
	// dstHost := cleaned.Host // Destination address, e.g. 127.0.0.1:5984.

	// Copy headers from client req into cleaned req, replacing Location header value if found.
	copyheadersIn(req.Header, cleaned.Header)
	cleaned.Proto = req.Proto
	cleaned.ContentLength = req.ContentLength

	if remoteAddrNoPort, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		cleaned.Header.Add("X-Forwarded-For", remoteAddrNoPort)
	}

	s.addXOriginalPath(req, cleaned)

	resp, err := s.httpTransport.RoundTrip(cleaned)
	if err != nil {
		s.errorLog.Info("HTTP RoundTrip error: " + err.Error())
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	var responseWriter io.Writer = w
	if resp.Body != nil {

		// Only compress when it hasn't been already and the client supports it.
		if resp.Header.Get("Content-Encoding") == "" && strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {

			// Remove any possible metadata in the header value, e.g. "text/html; charset=utf-8" becomes "text/html".
			responseContentType := resp.Header.Get("Content-Type")
			var trimmedContentType string
			sepIdx := strings.Index(responseContentType, ";")
			if sepIdx > 0 {
				trimmedContentType = responseContentType[:sepIdx]
			} else {
				trimmedContentType = responseContentType
			}

			// Only compress when content type is known and whitelisted.
			if _, allowed := s.configHttp.AutomaticGzip.whitelistMap[trimmedContentType]; allowed {
				// If we compress a response that is not chunked, then the original content length header is invalid.
				// We also do not know what the final length of the compressed content will be,
				// unless we zip to a buffer first and then write that to the response.
				// But we want to avoid a buffer for performance reasons.
				// It seems either the Go runtime or browser calculates and inserts the header automatically
				// at some point, so we just delete it here.
				resp.Header.Del("Content-Length")

				zipper := gzip.NewWriter(w)
				defer zipper.Close()
				responseWriter = zipper

				if resp.Header.Get("Vary") == "" {
					resp.Header.Add("Vary", "Accept-Encoding")
				}
				resp.Header.Set("Content-Encoding", "gzip")
			}
		}
	}

	// Copy headers from response into w, replacing Location header value back to original if found.
	copyHeaders(resp.Header, w.Header())
	w.WriteHeader(resp.StatusCode)

	if resp.Body != nil {
		defer resp.Body.Close()
		written, err := io.Copy(responseWriter, resp.Body)
		if err != nil {
			s.errorLog.Info("Failed to copy response body: " + err.Error())
			return
		}
		if resp.ContentLength > 0 && written != resp.ContentLength {
			s.errorLog.Infof("Incorrect amount of data copied from response body: Content-Length %v, Copied %v", resp.ContentLength, written)
			return
		}
	}
}

/*
forwardWebsocket does for websockets what forwardHTTP does for http requests. A new socket connection is made to the backend and messages are forwarded both ways.
*/
func (s *Server) forwardWebsocket(w http.ResponseWriter, req *http.Request, newurl string) {

	myHandler := func(con *websocket.Conn) {
		origin := "http://localhost"
		config, errCfg := websocket.NewConfig(newurl, origin)
		copyHeaders(req.Header, config.Header)
		if errCfg != nil {
			s.errorLog.Errorf("Error with config: %v\n", errCfg)
			return
		}
		backend, errOpen := websocket.DialConfig(config)
		if errOpen != nil {
			s.errorLog.Errorf("Error with websocket.DialConfig: %v\n", errOpen)
			return
		}
		copy := func(fromSocket *websocket.Conn, toSocket *websocket.Conn, done chan bool) {

			for {
				var data string
				var err error
				err = websocket.Message.Receive(fromSocket, &data)
				if err == io.EOF {
					fromSocket.Close()
					toSocket.Close()
					break
				}
				if err != nil && err != io.EOF {
					break
				}
				if e := websocket.Message.Send(toSocket, data); e != nil {
					break
				}
			}

			done <- true
		}

		tobackend := make(chan bool)
		go copy(con, backend, tobackend)
		frombackend := make(chan bool)
		go copy(backend, con, frombackend)
		<-tobackend
		<-frombackend
	}

	wsServer := &websocket.Server{}
	wsServer.Handler = myHandler
	wsServer.ServeHTTP(w, req)
}

// forwardUDP does for UDP what forwardHTTP does for http requests. UDP is connectionless, so implementation is very simple
func (s *Server) forwardUDP(w http.ResponseWriter, req *http.Request, newurl string) {
	u, err := url.Parse(newurl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dstHost := u.Host // Destination address, e.g. 127.0.0.1:5984.
	msg, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = s.udpConnPool.Send(dstHost, msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// addXOriginalPath adds a new HTTP header called X-Original-Path.
// This is here so that the service can see the exact URL Path which the client used when making
// the request to the router. If the client is signing the request, then she will use that
// original URL Path to sign the request. When the receiving service wants to validate the
// signature, it needs to know what Path to use when performing the validation. If the receiver
// were to use the replaced path, then the validation would fail.
// Example original Path: /crud/reload_schema
// Example replaced Path: /reload_schema
// In this example, we will set "X-Original-Path: /crud/reload_schema"
// original and modified may be the same object
func (s *Server) addXOriginalPath(original *http.Request, modified *http.Request) {
	// I originally thought that RawPath was the right thing to use here, but it turns out that url.Parse/url.ParseRequestURI will only set
	// RawPath if EscapedPath() is different from RawPath. This header was originally added for our request signing system, so that
	// the receiver can get access to the original URL string, the way the sender composed it. It looks like the best way to do that
	// is to parse the RequestURI ourselves, by looking for the ? and just using everything before that.
	rawPath := original.RequestURI
	if question := strings.IndexRune(original.RequestURI, '?'); question != -1 {
		rawPath = original.RequestURI[:question]
	}
	// fmt.Printf("%v\n", rawPath)
	modified.Header.Add("X-Original-Path", rawPath)
}

// Returns true if the request should continue to be passed through the router
// We make a round-trip to imqsauth here to check the credentials of the incoming request.
// This adds about a 0.5ms latency to the request. It might be worthwhile to embed
// imqsauth inside imqsrouter.
func (s *Server) authorize(w http.ResponseWriter, req *http.Request, requirePermission string) (authData *serviceauth.Token, authOK bool) {
	if requirePermission == "" {
		return nil, true
	}

	if err := serviceauth.VerifyInterServiceRequest(req); err == nil {
		return nil, true
	}

	if httpCode, errorMsg, authData := serviceauth.VerifyUserHasPermission(req, requirePermission); httpCode == http.StatusOK {
		return authData, true
	} else { // Not OK
		if httpCode == http.StatusUnauthorized {
			s.errorLog.Info(errorMsg) // we expect some unauthorized requests, so don't log them as errors
		} else {
			s.errorLog.Error(errorMsg)
		}
		http.Error(w, errorMsg, httpCode)
		return nil, false
	}
}

func (s *Server) Pong(w http.ResponseWriter, req *http.Request) {
	timestamp := time.Now().Unix()
	fmt.Fprintf(w, `{"Timestamp":%v}`, timestamp)
}

func (s *Server) close() {
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func copyCookieToMSHTTP(org *http.Cookie) *http.Cookie {
	c := &http.Cookie{
		Name:  org.Name,
		Value: org.Value,
	}
	return c
}

func copyheadersIn(src http.Header, dst http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			if k == "Connection" && v == "close" {
				// See detailed explanation in top-level function comment
				continue
			}
			dst.Add(k, v)
		}
	}
}

func copyHeaders(src http.Header, dst http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func openLog(filename string, defaultWriter io.Writer) io.Writer {
	if filename == "" {
		return defaultWriter
	}
	return &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    50, // megabytes
		MaxBackups: 3,
		MaxAge:     90, // days
	}
}
