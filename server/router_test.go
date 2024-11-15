package server

/*
In all tests the environment looks like the following :

 --------          --------             --------------
| client |  <-->  | router |  <------> | http backend |
 -------           --------             --------------
                      ^
                      |                 -------------------
                       --------------> | websocket backend |
                                        -------------------

Http:
Requests are send to the router which routes them to the backend and creates a response body
with the following format "METHOD <method> URL <backend received url> BODY <backend received body>",
this is then returned to the router which in turn returns it to client for checking.

Websocket:
Same as above but since there are no headers or methods in websockets the message received by the
backend are return to via the router to the client websocket.

Killing A Server

At the time of going to press, there is no way to kill a Go HTTP server, if you've started it with
ListenAndServe or ListenAndServeTLS. You may be tempted to implement your own version of those two
functions, and that's OK for the HTTP case, but for the HTTPS case, if you do implement your own,
you lose HTTP/2 functionality. Initially we followed this approach, but when HTTP/2 came around,
we needed to abandon it.

Long story short - we cannot kill our HTTP server inside the unit test framework, but we don't actually
need to, because the Go test framework launches a separate process for each test.
*/

import (
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const mainConfig = `
{
	"AccessLog":		"router-access-test.log",
	"ErrorLog":			"router-error-test.log",
	"HTTP": {
		"Port": 5002
	},
	"Targets": {
		"PORT5000": {
			"URL": "http://127.0.0.1:5000"
		}
	},
	"Routes": {
		"/test(.*)":				"{PORT5000}/test$1",
		"/test1/(.*)":				"{PORT5000}/test1/$1",
		"/test2/(.*)":				"{PORT5000}/redirect2/$1",
		"/test3/(.*)":				"{PORT5000}/$1",
		"/nominatim/(.*)":			"https://nominatim.openstreetmap.org/$1",
		"/geonames/(.*)":			"http://api.geonames.org/geonames/$1",
		"/wws/(.*)":				"ws://127.0.0.1:5100/wws/$1"
	}
}
`

type sandbox struct {
	front *Server
	back  *backend
}

func httpStart(t *testing.T) func(t *testing.T) {
	s := &sandbox{}
	s.back = newBackend()
	s.back.Addr = ":5000"
	go s.back.ListenAndServe()

	config := &Config{}
	err := config.LoadString(mainConfig)
	if err != nil {
		t.Fatal("Could not load config")
	}
	if s.front, err = NewServer(config); err != nil {
		t.Fatal("Could not start server")
	}

	go s.front.ListenAndServe()
	time.Sleep(500 * time.Millisecond)
	return func(t *testing.T) {
		s.back.Close()
	}
}

func doHttp(t *testing.T, method, url, body, expect_body string) {
	doHttpFunc(t, method, url, body, func(t *testing.T, resp_body string) {
		if resp_body != expect_body {
			t.Errorf("Expected \"%s\" received \"%s\"", expect_body, resp_body)
		}
	})
}

func doHttpFunc(t *testing.T, method, url, body string, verifyBodyFunc func(*testing.T, string)) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request '%v' failed with: %v", url, err)
	}
	body_response, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	verifyBodyFunc(t, string(body_response))
}

func TestVariousURL(t *testing.T) {
	close := httpStart(t)
	defer close(t)
	doHttp(t, "GET", "http://127.0.0.1:5002/gert/jan/piet", "", "Route not found\n")                                                       // Invalid route
	doHttp(t, "GET", "http://127.0.0.1:5002/test1", "", "Method GET URL /test1 BODY ")                                                     // hello world
	doHttp(t, "GET", "http://127.0.0.1:5002/test2/path1/path2", "", "Method GET URL /redirect2/path1/path2 BODY ")                         // replace base url
	doHttp(t, "GET", "http://127.0.0.1:5002/test3/and/some/other/path/elements", "", "Method GET URL /and/some/other/path/elements BODY ") // remove base url
	doHttp(t, "GET", "http://127.0.0.1:5002/test1/testbody", "SomeBodyText", "Method GET URL /test1/testbody BODY SomeBodyText")           // body
	doHttp(t, "GET", "http://127.0.0.1:5002/test1/and/a/further/very/long/url/this/can/go/up/to/11kilobits/", "", "Method GET URL /test1/and/a/further/very/long/url/this/can/go/up/to/11kilobits/ BODY ")

	// other host
	doHttpFunc(t, "GET", "http://127.0.0.1:5002/nominatim/search.php?q=TechnoPark,+Stellenbosch&format=json", "", func(t *testing.T, resp_body string) {
		if strings.Index(resp_body, "Cape Winelands") == -1 {
			t.Errorf("nominatim search failed. Response body: %v", resp_body)
		}
	})
}

func TestMethods(t *testing.T) {
	close := httpStart(t)
	defer close(t)
	methods := [4]string{"GET", "DELETE", "POST", "PUT"}
	expected := [4]string{
		"Method GET URL /test1/testbody BODY SomeBodyText",
		"Method DELETE URL /test1/testbody BODY SomeBodyText",
		"Method POST URL /test1/testbody BODY SomeBodyText",
		"Method PUT URL /test1/testbody BODY SomeBodyText"}
	for index, method := range methods {
		doHttp(t, method, "http://127.0.0.1:5002/test1/testbody", "SomeBodyText", expected[index])
	}
}

/*
Im leaving this out as it is more a test of the testbox and the tcp protocol than router,
leaves lots of sockets in time_wait state, allowing following test to fail if run
a couple of times. This must definitely be run on the server with the tests running on another client box.
func TestManyClientSingleRequest(t *testing.T) {
	//Startup()
	var clientGroup sync.WaitGroup
	many := func(t *testing.T) {
		defer clientGroup.Done()
		const expected = "/redirect2/path1/path2"
		client := &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		}
		resp, err := client.Get("http://127.0.0.1:5002/test2/path1/path2")
		if err != nil {
			t.Error(err)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}
		resp.Body.Close()
		if !bytes.Equal(body, []byte(expected)) {
			t.Errorf("Expected %s received %s", expected, body)
		}
	}
	for i := 0; i < 1000; i++ {
		clientGroup.Add(1)
		go many(t)
	}
	clientGroup.Wait()
	//Shutdown()
}
*/
/*
// Leaving this out for same as above reason
func TestSingleClientManyRequests(t *testing.T) {
	//Startup()
	// Many request - single client
	var clientGroup sync.WaitGroup
	client := &http.Client{}
	many := func(t *testing.T) {
		defer clientGroup.Done()
		const expected = "/redirect2/path1/path2"
		resp, err := client.Get("http://127.0.0.1:5002/test2/path1/path2")
		if err != nil {
			t.Error(err)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}
		resp.Body.Close()
		if !bytes.Equal(body, []byte(expected)) {
			t.Errorf("Expected %s received %s", expected, body)
		}
	}
	for i := 0; i < 1000; i++ {
		clientGroup.Add(1)
		go many(t)
	}
	clientGroup.Wait()
	//Shutdown()
}
*/

////////////////////////////////////////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// Very simple backend server to use for testing.
type backend struct {
	*http.Server
}

func newBackend() *backend {
	b := &backend{}
	b.Server = &http.Server{}
	b.Server.Handler = b
	return b
}

func (b *backend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	req.Body.Close()

	fmt.Fprintf(w, "Method %s URL %s BODY %s", req.Method, html.EscapeString(req.URL.Path), body)
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func wsEchoHandler(ws *websocket.Conn) {
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			log.Printf("EchoServer Receive : %v\n", err)
			break
		}
		msg = "Backend Websocket Received : " + msg
		if err := websocket.Message.Send(ws, msg); err != nil {
			log.Printf("EchoServer Send : %v\n", err)
			break
		}
	}
}

// simple websocket backend
func wsServer(t *testing.T) func(t *testing.T) {
	m := http.NewServeMux()
	m.Handle("/wws/", websocket.Handler(wsEchoHandler))
	s := http.Server{
		Addr:    ":5100",
		Handler: m,
	}
	go s.ListenAndServe()

	return func(t *testing.T) {
		s.Close()
	}
}

func TestWebsocket(t *testing.T) {
	expected := "Backend Websocket Received : testing webserver"
	cleanup := wsServer(t)
	origin := "http://localhost/"
	url := "ws://127.0.0.1:5002/wws/x"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatalf("Initial dial failed: %v", err)
	}
	msg := "testing webserver"
	if e := websocket.Message.Send(ws, msg); e != nil {
		t.Fatal(e)
	}

	if e := websocket.Message.Receive(ws, &msg); e != nil {
		t.Fatal(e)
	}
	if msg != expected {
		t.Errorf("Expected %s received %s", expected, msg)
	}

	msg = "testing webserver"
	if e := websocket.Message.Send(ws, msg); e != nil {
		t.Fatal(e)
	}

	if e := websocket.Message.Receive(ws, &msg); e != nil {
		t.Fatal(e)
	}

	if msg != expected {
		t.Errorf("Expected \"%s\" received \"%s\"", expected, msg)
	}
	cleanup(t)
	ws.Close()
}
