# Test Folder

This folder contains utilities for testing CORS (Cross-Origin Resource Sharing) settings on a remote server.

## Files

### servefile.go

A minimal Go HTTP server that serves the `test.html` file on
`http://localhost:8080`. This allows you to easily load the test page in your
browser.

### test.html

A simple HTML page with JavaScript that lets you specify a remote server URL and
make a cross-origin request to it. This helps you verify if your target server's
CORS settings are correct by observing the response and any errors.  
Make sure your of your working directory to avoid path errors.

## Usage
1. Run `servefile.go` with `go run servefile.go`.
2. Open `http://localhost:8080` in your browser.
3. Enter the remote server URL you want to test and click "Test CORS".
4. Observe the results and adjust your target server's CORS configuration as needed.

