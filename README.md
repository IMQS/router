Router
======
This is an umbrella project that wraps up [router-core](https://github.com/IMQS/router-core)

Building
--------
* Run `env.bat`
* Run `go run src\github.com\IMQS\router-core\socklisten.go`. This will listen on localhost:8081
* Run `go run src\github.com\IMQS\router-core\router.go`. This will listen on localhost:8080
* You can now point Chrome at `localhost:8080` and in the Chrome developer console you should see messages
about communicating with a websocket. The 'socklisten' application will also spit out messages to stdout.

To run SublimeText, you'll want to launch it from the command line,
after running 'env.bat', so that your GOPATH is correct for GoSublime's sake.

Dependencies
------------
We choose to bake the websocket library into this project to make 
continuous integration easier. The websocket code lives in a mercurial repo,
and we choose not to introduce a dependency on git-hg.

To update the Mercurial dependency:

* Run env.bat
* `go get code.google.com/p/go.net/websocket`

The Git-based dependencies are all stored using regular git submodules.
To update them, just follow the regular method to update a submodule
inside a git repository.