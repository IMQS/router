package main

import (
	"fmt"
	"net/http"
)

type TS struct {
}

func (TS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "test.html")
}

func main() {
	fmt.Println("Starting static file server...")
	testServe := TS{}
	fmt.Println("CORS test web page running on http://localhost:8080")
	http.ListenAndServe(":8080", testServe)
}
