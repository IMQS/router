package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IMQS/gowinsvc/service"
	"github.com/IMQS/router/server"
)

func main() {
	os.Exit(realMain())
}

func realMain() (result int) {
	result = 0

	defer func() {
		if err := recover(); err != nil {
			result = 1
			fmt.Printf("%v\n", err)
		}
	}()

	flags := flag.NewFlagSet("router", flag.ExitOnError)
	configFile := flags.String("config", "", "Optional config file for testing")
	showHttpPort := flags.Bool("show-http-port", false, "print the http port to stdout and exit")

	if len(os.Args) > 1 {
		flags.Parse(os.Args[1:])
	}

	config := &server.Config{}

	err := config.LoadFile(*configFile)
	if err != nil {
		panic(fmt.Errorf("Error loading '%s': %v", *configFile, err))
	}

	if os.Getenv("DISABLE_HTTPS_REDIRECT") == "1" {
		config.HTTP.RedirectHTTP = false
	}

	envHttpPort := os.Getenv("HTTP_PORT")
	if envHttpPort != "" {
		port, err := strconv.ParseInt(envHttpPort, 10, 64)
		if err != nil {
			panic(fmt.Errorf("Invalid HTTP_PORT environment variable '%v'", envHttpPort))
		}
		config.HTTP.Port = uint16(port)
	}

	if *showHttpPort {
		fmt.Printf("%v", config.HTTP.GetPort())
		result = 0
		return
	}

	var authChannel chan string
	authChannel = make(chan string, 1000)

	// authConsumer
	server, err := server.NewServer(config, authChannel)
	go func() {
		cnt := 0
		var s string
		var ok bool
		var buff []string
		waitTime := time.Now()
		dumpAndExit := false
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Recovered in f: %v\n", r)
				fmt.Printf("ERROR in authConsumer, existing goroutine\n")
				//s := GetStack()
				//errGlobal = fmt.Errorf(fmt.Sprintf("%v\n%v\n", r, s))
			}
		}()
		for {
			//s, ok = <-authChannel
			select {
			case s, ok = <-authChannel:
				if !ok {
					dumpAndExit = true
				} else {
					buff = append(buff, s)
					cnt++
					//fmt.Printf("cnt: %v\n", cnt)
				}
				newTime := time.Now()
				// this scheme has the effect of flushing the buffer every 100 messages or every 10ms
				// if absolutely nothing is received for a long time via the channel (but the service does not terminate)
				// then the buffer may never be flushed...
				// to get around this we may introduce another channel with a ticker
				// every second

				diff := newTime.Sub(waitTime)
				if (cnt > 100) || diff > (100*time.Millisecond) || dumpAndExit {
					cnt = 0
					waitTime = time.Now()
					fmt.Printf("Dump: cnt %v, diff: %s\n", cnt, diff)
					fmt.Printf("%v\n", strings.Join(buff, "\n"))
					buff = nil
				}
				if dumpAndExit {
					fmt.Printf("Exiting authConsumer\n")
					return
				}
			}
		}
	}()

	if err != nil {
		panic(fmt.Errorf("Error starting server: %v", err))
	}

	handler := func() error {
		return server.ListenAndServe()
	}

	handlerNoRet := func() {
		handler()
	}
	success := true
	if !service.RunAsService(handlerNoRet) {
		// Run in the foreground
		success = false
		fmt.Print(handler())
	}

	if success {
		result = 0
	} else {
		result = 1
	}
	return
}
