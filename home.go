package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func serveHome(w http.ResponseWriter, r *http.Request) {
	defer observePage("home", time.Now())

	if r.Method != "GET" {
		http.Error(w, "405 - Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "/" {
		m := r.FormValue("m")
		if m != "" {
			http.Redirect(w, r, "/"+m, http.StatusTemporaryRedirect)
			return
		}

		recentBuilds.Lock()
		recentLinks := append([]string{}, recentBuilds.links...)
		recentBuilds.Unlock()

		// Reverse order for recentLinks most recent first.
		n := len(recentLinks)
		for i := 0; i < n/2; i++ {
			j := n - 1 - i
			recentLinks[i], recentLinks[j] = recentLinks[j], recentLinks[i]
		}

		var args = struct {
			Favicon        string
			Recents        []string
			VerifierKey    string
			GobuildVersion string
		}{
			"favicon.ico",
			recentLinks,
			config.VerifierKey,
			gobuildVersion,
		}
		if err := homeTemplate.Execute(w, args); err != nil {
			failf(w, "%w: executing template: %v", errServer, err)
		}
		return
	}

	t := strings.Split(r.URL.Path[1:], "/")
	if !strings.Contains(t[0], ".") {
		http.NotFound(w, r)
		return
	}

	if !strings.Contains(r.URL.Path, "@") {
		serveModules(w, r)
		return
	}

	req, hint, ok := parseRequest(r.URL.Path)
	if !ok {
		if hint != "" {
			http.Error(w, fmt.Sprintf("404 - File Not Found\n\n%s\n", hint), http.StatusNotFound)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	what := "build"
	if req.Sum != "" {
		what = "result"
	}
	defer observePage(what+" "+req.Page.String(), time.Now())
	if req.Sum == "" {
		serveBuild(w, r, req)
	} else {
		serveResult(w, r, req)
	}
}
