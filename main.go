package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

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

	server, err := server.NewServer(config)
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
