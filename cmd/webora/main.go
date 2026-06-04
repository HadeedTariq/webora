package main

import "time"

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
)
