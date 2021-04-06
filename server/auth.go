package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IMQS/log"
	"github.com/IMQS/serviceauth"
)

/*
Sample PureHub response:
{
	"access_token": "a-long-token",
	"token_type": "bearer",
	"expires_in": 3599,
	"userName": "user@example.com",
	".issued": "Thu, 12 Feb 2015 12:15:23 GMT",
	".expires": "Thu, 12 Feb 2015 13:15:23 GMT"
}
*/
type pureHubAuthResponse struct {
	AccessToken string `json:"access_token"`
	Expires     string `json:".expires"`
}

// Returns true if the request should continue to be passed through the router
// If you return false, then you must already have sent an appropriate error response to 'w'.
func authPassThrough(log *log.Logger, w http.ResponseWriter, req *http.Request, authData *serviceauth.Token, target *targetPassThroughAuth) bool {
	switch target.config.Type {
	case AuthPassThroughNone:
		return true
	case AuthPassThroughPureHub:
		return authInjectPureHub(log, w, req, target)
	case AuthPassThroughSitePro:
		return authInjectSitePro(log, w, req, target)
	case AuthPassThroughECS:
		return authInjectECS(log, w, req, target)
	case AuthPassThroughCouchDB:
		return authInjectCouchDB(log, w, req, authData, target)
	default:
		return true
	}
}

func authInjectECS(log *log.Logger, w http.ResponseWriter, req *http.Request, target *targetPassThroughAuth) bool {
	req.SetBasicAuth(target.config.Username, target.config.Password)

	actionUrl := strings.Split(req.URL.String(), "/") // Example: "ecs/ACCESS/FWVERSION/" or "ecs/sam/ForceSim1/"
	context := "{}"
	didWhat := req.URL.String()
	toWhat := req.URL.String()
	if len(actionUrl) == 5 && actionUrl[1] == "ecs" {
		context = `{"url": "` + req.URL.String() + `","origin": "ecs api passthrough router"}`
		didWhat = actionUrl[3]
		switch actionUrl[2] {
		case "ACCESS":
			toWhat = "site gate: " + actionUrl[4]
		case "sam":
			toWhat = "site: " + actionUrl[4]
		default:
			http.Error(w, "Unkown url to ECS API", http.StatusBadRequest) // Don't allow unkown url's to ECS API
			return false
		}
	} else {
		http.Error(w, "Unkown url to ECS API", http.StatusBadRequest) // Don't allow unkown url's to ECS API
		return false
	}

	statusCode, err := serviceauth.AddToAuditLog(req, didWhat, toWhat, context)
	if err != nil {
		log.Errorf("Error logging user action")
		http.Error(w, err.Error(), statusCode)
		return false
	}
	return true
}

func authInjectSitePro(log *log.Logger, w http.ResponseWriter, req *http.Request, target *targetPassThroughAuth) bool {
	req.SetBasicAuth(target.config.Username, target.config.Password)
	return true
}

func authInjectCouchDB(log *log.Logger, w http.ResponseWriter, req *http.Request, authData *serviceauth.Token, target *targetPassThroughAuth) bool {
	// Allow pings to the CouchDB service
	if req.URL.Path == "/userstorage/" {
		return true
	}

	req.SetBasicAuth(target.config.Username, target.config.Password)

	splitPath := strings.SplitAfter(req.URL.Path, "userdb-")
	userIDSplitPath := strings.Split(splitPath[1], "/")
	userIDPath, _ := strconv.Atoi(userIDSplitPath[0])

	// Ensure logged-in user can only access his own data
	if authData.UserID == userIDPath {
		return true
	}
	return false
}

func authInjectPureHub(log *log.Logger, w http.ResponseWriter, req *http.Request, target *targetPassThroughAuth) bool {
	// The 'inject' function assumes you have obtained a lock (read or write) on "target.lock"
	inject := func() {
		req.Header.Set("Authorization", "Bearer "+target.token)
	}

	// Run with two attempts.
	// First attempt is optimistic. We take the read lock, and inject the auth header if it is valid.
	// On the second attempt we take the write lock, and generate a new token.

	done := false
	target.lock.RLock()
	if target.token != "" && target.tokenExpires.After(time.Now()) {
		done = true
		inject()
	}
	target.lock.RUnlock()
	if done {
		return true
	}

	// Acquire a new token
	target.lock.Lock()
	err := pureHubGetToken(log, target)
	if err == nil {
		log.Infof("Success acquiring PureHub authentication token")
		inject()
	} else {
		log.Infof("Error acquiring PureHub authentication token: %v", err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
	}
	target.lock.Unlock()

	return err != nil
}

func pureHubGetToken(log *log.Logger, target *targetPassThroughAuth) error {
	requestBody := "grant_type=password&username=" + url.QueryEscape(target.config.Username) + "&password=" + url.QueryEscape(target.config.Password)
	resp, err := http.Post(target.config.LoginURL, "application/x-www-form-urlencoded", strings.NewReader(requestBody))
	if err != nil {
		return fmt.Errorf("http.Post: %v", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err == nil {
		var token pureHubAuthResponse
		if err = json.Unmarshal(body, &token); err == nil {
			target.token = token.AccessToken
			target.tokenExpires, err = time.Parse(time.RFC1123, token.Expires)
			if err == nil {
				// Lower the possibility of using an expired token. We happen to know that they last one hour,
				// so chopping one minute off it should be fine.
				target.tokenExpires = target.tokenExpires.Add(-60 * time.Second)
			}
		} else {
			return fmt.Errorf("Error decoding JSON: %v", err)
		}
	} else {
		return fmt.Errorf("%v: %v", resp.Status, err)
	}
	return err
}
