package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/mod/module"
)

type modVersion struct {
	Version string
	Time    time.Time
}

func resolveModuleLatest(ctx context.Context, goproxy, mod string) (*modVersion, error) {
	t0 := time.Now()
	defer func() {
		metricGoproxyLatestDuration.Observe(time.Since(t0).Seconds())
	}()

	modPath, err := module.EscapePath(mod)
	if err != nil {
		return nil, fmt.Errorf("bad module path: %v", err)
	}
	u := fmt.Sprintf("%s%s/@latest", goproxy, modPath)
	mreq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: preparing goproxy http request: %v", errServer, err)
	}
	mreq.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(mreq)
	if err != nil {
		return nil, fmt.Errorf("%w: http request to goproxy: %v", errServer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		metricGoproxyLatestErrors.WithLabelValues(fmt.Sprintf("%d", resp.StatusCode)).Inc()
		buf, err := io.ReadAll(resp.Body)
		msg := string(buf)
		if err != nil {
			msg = fmt.Sprintf("reading error message: %v", err)
		}
		return nil, fmt.Errorf("%w: error response from goproxy, status %s:\n%s", errRemote, resp.Status, msg)
	}
	var info modVersion
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("%w: parsing json returned by goproxy: %v", errRemote, err)
	} else if info.Version == "" {
		return nil, fmt.Errorf("%w: empty version from goproxy", errRemote)
	}
	return &info, nil
}
