package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HadeedTariq/webora/pkg/values"
)

const (
	appName        = "Webora"
	appSite        = "https://github.com/HadeedTariq/webora"
	defaultDelay   = 150 * time.Millisecond
	defaultTimeout = 5 * time.Second
)

var (
	GitTag    string
	GitHash   string
	BuildDate string
	defaultUA = "Mozilla/5.0 (compatible; Win64; x64) Mr." + appName + "/" + GitTag + "-" + GitHash
)

// so the command line flags are liked
var (
	fDepth, fWorkers        int
	fSilent, fVersion       bool
	fBrute, fNoHeads        bool
	fSkipSSL, fScanJS       bool
	fScanCSS, fScanALL      bool
	fSubdomains             bool
	fDirsPolicy, fProxyAuth string
	fRobotsPolicy, fUA      string
	fDelay                  time.Duration
	fTimeout                time.Duration
	// for reading the files which references  are passed through the cli
	cookies, headers values.Smart
	tags, ignored    values.List
)

func version() string {
	return fmt.Sprintf("%s %s-%s build at: %s with %s site: %s",
		appName,
		GitTag,
		GitHash,
		BuildDate,
		runtime.Version(),
		appSite,
	)
}

func usage() {
	var sb strings.Builder

	fmt.Fprintf(&sb, "%s - the unix-way web crawler, usage:\n\n", appName)
	fmt.Fprintf(&sb, "%s [flags] url\n\n", filepath.Base(os.Args[0]))
	fmt.Fprint(&sb, "possible flags with default values:\n\n")

	_, _ = os.Stderr.WriteString(sb.String())

	flag.PrintDefaults()
}

func puts(s string) {
	_, _ = os.Stdout.WriteString(s + "\n")
}

func loadSmart() (h, c []string, err error) {
	var wd string

	if wd, err = os.Getwd(); err != nil {
		err = fmt.Errorf("work dir: %w", err)

		return
	}

	fs := os.DirFS(wd)

	if h, err = headers.Load(fs); err != nil {
		err = fmt.Errorf("headers: %w", err)

		return
	}

	if c, err = cookies.Load(fs); err != nil {
		err = fmt.Errorf("cookies: %w", err)

		return
	}

	return h, c, nil
}

func setupFlags() {
	flag.Var(&headers, "header",
		"extra headers for request, can be used multiple times, accept files with '@'-prefix",
	)
	flag.Var(&cookies, "cookie",
		"extra cookies for request, can be used multiple times, accept files with '@'-prefix",
	)

	flag.Var(&tags, "tag", "tags filter, single or comma-separated tag names")
	flag.Var(&ignored, "ignore", "patterns (in urls) to be ignored in crawl process")

	flag.IntVar(&fDepth, "depth", 0, "scan depth (set -1 for unlimited)")
	flag.IntVar(&fWorkers, "workers", runtime.NumCPU(), "number of workers")

	flag.BoolVar(&fScanALL, "all", false, "scan all known sources (js/css/...)")
	flag.BoolVar(&fBrute, "brute", false, "scan html comments")
	flag.BoolVar(&fSubdomains, "subdomains", false, "Support subdomains (e.g. if www.domain.com found, recurse over it)")
	flag.BoolVar(&fScanCSS, "css", false, "scan css for urls")
	flag.BoolVar(&fNoHeads, "headless", false, "disable pre-flight HEAD requests")
	flag.BoolVar(&fScanJS, "js", false, "scan js code for endpoints")
	flag.BoolVar(&fSkipSSL, "skip-ssl", false, "skip ssl verification")
	flag.BoolVar(&fSilent, "silent", false, "suppress info and error messages in stderr")
	flag.BoolVar(&fVersion, "version", false, "show version")

	flag.StringVar(&fUA, "user-agent", defaultUA, "user-agent string")
	flag.StringVar(&fProxyAuth, "proxy-auth", "", "credentials for proxy: user:password")

	flag.DurationVar(&fDelay, "delay", defaultDelay, "per-request delay (0 - disable)")
	flag.DurationVar(&fTimeout, "timeout", defaultTimeout, "request timeout (min: 1 second, max: 10 minutes)")

	flag.Usage = usage
}
