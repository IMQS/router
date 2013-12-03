Router
======
This is an umbrella project that wraps up [router-core](https://github.com/IMQS/router-core)

Building
--------
* Run `env.bat`
* Run `go install github.com/IMQS/router-core`
This will leave you with the executable `bin/router-core`

To run SublimeText, you'll want to launch it from the command line,
after running 'env.bat', so that your GOPATH is correct for GoSublime's sake.

Dependencies
------------
We choose to bake the websocket and winsvc libraries into this project to make 
continuous integration easier. These libraries live in a mercurial repo,
and we choose not to introduce a dependency on git-hg.

To update the Mercurial dependency:

* Run env.bat
* `go get code.google.com/p/go.net/websocket`
* `go get code.google.com/p/winsvc`

The Git-based dependencies are all stored using regular git submodules.
To update them, just follow the regular method to update a submodule
inside a git repository.