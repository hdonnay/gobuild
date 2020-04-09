package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjl-/goreleases"
)

type target struct {
	Goos   string
	Goarch string
}

func (t target) osarch() string {
	return t.Goos + "/" + t.Goarch
}

// List of targets from "go tool dist list", bound to be out of date here; should probably generate on startup, or when we get the first sdk installed.
// Android and darwin/arm* cannot build on my linux/amd64 machine.
// Note: list will be sorted after startup by readRecentBuilds, most used first.
type xtargets struct {
	sync.Mutex
	use      map[string]int // Used for popularity, and for validating build requests.
	totalUse int
	list     []target
}

var targets = &xtargets{
	sync.Mutex{},
	map[string]int{},
	0,
	[]target{
		{"aix", "ppc64"},
		//	{"android", "386"},
		//	{"android", "amd64"},
		//	{"android", "arm"},
		//	{"android", "arm64"},
		{"darwin", "386"},
		{"darwin", "amd64"},
		//	{"darwin", "arm"},
		//	{"darwin", "arm64"},
		{"dragonfly", "amd64"},
		{"freebsd", "386"},
		{"freebsd", "amd64"},
		{"freebsd", "arm"},
		{"freebsd", "arm64"},
		{"illumos", "amd64"},
		{"js", "wasm"},
		{"linux", "386"},
		{"linux", "amd64"},
		{"linux", "arm"},
		{"linux", "arm64"},
		{"linux", "mips"},
		{"linux", "mips64"},
		{"linux", "mips64le"},
		{"linux", "mipsle"},
		{"linux", "ppc64"},
		{"linux", "ppc64le"},
		{"linux", "riscv64"},
		{"linux", "s390x"},
		{"netbsd", "386"},
		{"netbsd", "amd64"},
		{"netbsd", "arm"},
		{"netbsd", "arm64"},
		{"openbsd", "386"},
		{"openbsd", "amd64"},
		{"openbsd", "arm"},
		{"openbsd", "arm64"},
		{"plan9", "386"},
		{"plan9", "amd64"},
		{"plan9", "arm"},
		{"solaris", "amd64"},
		{"windows", "386"},
		{"windows", "amd64"},
		{"windows", "arm"},
	},
}

func init() {
	for _, t := range targets.list {
		targets.use[t.osarch()] = 0
	}
}

func (t *xtargets) get() []target {
	t.Lock()
	defer t.Unlock()
	return t.list
}

func (t *xtargets) valid(target string) bool {
	t.Lock()
	defer t.Unlock()
	_, ok := t.use[target]
	return ok
}

// must be called with lock held.
func (t *xtargets) sort() {
	n := make([]target, len(t.list))
	copy(n, t.list)
	sort.Slice(n, func(i, j int) bool {
		return t.use[n[i].osarch()] > t.use[n[j].osarch()]
	})
	t.list = n
}

func (t *xtargets) increase(target string) {
	t.Lock()
	defer t.Unlock()
	t.use[target]++
	t.totalUse++
	if t.totalUse <= 32 || t.totalUse%32 == 0 {
		t.sort()
	}
}

var sdk struct {
	sync.Mutex
	installed     map[string]struct{}
	lastSupported time.Time // When last supported list was fetched. We fetch once per hour.
	supportedList []string  // List of latest supported releases, from https://golang.org/dl/?mode=json.
	installedList []string  // List of all other installed releases.

	fetch struct {
		sync.Mutex
		status map[string]error
	}
}

func initSDK() {
	sdk.installed = map[string]struct{}{}
	l, err := ioutil.ReadDir(config.SDKDir)
	if err != nil {
		log.Fatalf("readdir sdk: %v", err)
	}
	for _, e := range l {
		if strings.HasPrefix(e.Name(), "go") {
			sdk.installed[e.Name()] = struct{}{}
		}
	}
	sdkUpdateInstalledList()

	sdk.fetch.status = map[string]error{}
}

// Lock must be held by calling.
func sdkIsSupported(goversion string) bool {
	for _, e := range sdk.supportedList {
		if e == goversion {
			return true
		}
	}
	return false
}

// Lock must be held by calling.
func sdkUpdateInstalledList() {
	l := []string{}
	for goversion := range sdk.installed {
		if !sdkIsSupported(goversion) {
			l = append(l, goversion)
		}
	}
	sort.Slice(l, func(i, j int) bool {
		return l[j] < l[i]
	})
	sdk.installedList = l
}

func ensureMostRecentSDK() (string, error) {
	supported, _ := installedSDK()
	if len(supported) == 0 {
		return "", fmt.Errorf("%w: no supported go versions", errServer)
	}
	err := ensureSDK(supported[0])
	if err != nil {
		return "", err
	}
	return supported[0], nil
}

func installedSDK() (supported []string, remainingAvailable []string) {
	now := time.Now()
	sdk.Lock()
	if now.Sub(sdk.lastSupported) > time.Hour {
		// Don't hold lock while requesting. Don't let others make the same request.
		sdk.lastSupported = now
		sdk.Unlock()

		// todo: set a (low) timeout on the request
		rels, err := goreleases.ListSupported()
		sdk.Lock()

		if err != nil {
			log.Printf("listing supported go releases: %v", err)
		} else {
			sdk.supportedList = []string{}
			for _, rel := range rels {
				sdk.supportedList = append(sdk.supportedList, rel.Version)
			}
			sdkUpdateInstalledList()
		}
	}
	supported = sdk.supportedList
	remainingAvailable = sdk.installedList
	defer sdk.Unlock()
	return
}

var errBadGoversion = errors.New("bad goversion")

func ensureSDK(goversion string) error {
	// Reproducible builds work from go1.13 onwards. Refuse earlier versions.
	if !strings.HasPrefix(goversion, "go") {
		return fmt.Errorf(`%w: must start with "go"`, errBadGoversion)
	}
	if strings.HasPrefix(goversion, "go1") {
		if len(goversion) < 4 || !strings.HasPrefix(goversion, "go1.") {
			return fmt.Errorf("%w: old version, must be >=go1.13", errBadGoversion)
		}
		num, err := strconv.ParseInt(strings.Split(goversion[4:], ".")[0], 10, 64)
		if err != nil || num < 13 {
			return fmt.Errorf("%w: bad version, must be >=go1.13", errBadGoversion)
		}
	}

	// See if this is an SDK we know we have installed.
	sdk.Lock()
	if _, ok := sdk.installed[goversion]; ok {
		sdk.Unlock()
		return nil
	}
	sdk.Unlock()

	// Not installed yet. Let's see if we've fetched it before. If we tried and failed
	// before, we won't try again (during the lifetime of this process). If another
	// goroutine has installed it while we were waiting on the lock, we know this by
	// the presence of an entry in status, without an error.
	sdk.fetch.Lock()
	defer sdk.fetch.Unlock()
	err, ok := sdk.fetch.status[goversion]
	if ok {
		return err
	}

	rels, err := goreleases.ListAll()
	if err != nil {
		err = fmt.Errorf("%w: listing known releases: %v", errRemote, err)
		sdk.fetch.status[goversion] = err
		return err
	}
	for _, rel := range rels {
		if rel.Version == goversion {
			f, err := goreleases.FindFile(rel, runtime.GOOS, runtime.GOARCH, "archive")
			if err != nil {
				err = fmt.Errorf("%w: finding release file: %v", errServer, err)
				sdk.fetch.status[goversion] = err
				return err
			}
			tmpdir, err := ioutil.TempDir(config.SDKDir, "tmp-install")
			if err != nil {
				err = fmt.Errorf("%w: making tempdir for sdk: %v", errServer, err)
				sdk.fetch.status[goversion] = err
				return err
			}
			defer func() {
				os.RemoveAll(tmpdir)
			}()
			err = goreleases.Fetch(f, tmpdir, nil)
			if err != nil {
				err = fmt.Errorf("%w: installing sdk: %v", errServer, err)
				sdk.fetch.status[goversion] = err
				return err
			}
			err = os.Rename(filepath.Join(tmpdir, "go"), filepath.Join(config.SDKDir, goversion))
			if err != nil {
				err = fmt.Errorf("%w: putting sdk in place: %v", errServer, err)
			} else {
				sdk.Lock()
				defer sdk.Unlock()
				sdk.installed[goversion] = struct{}{}
				sdkUpdateInstalledList()
			}
			sdk.fetch.status[goversion] = err
			return err
		}
	}

	// Release not found. It may be a future release. Don't mark it as
	// tried-and-failed.
	// We may want to ratelimit how often we ask...
	return fmt.Errorf("%w: no such version", errBadGoversion)
}

func goexe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
