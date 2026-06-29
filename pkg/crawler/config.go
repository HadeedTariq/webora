package crawler

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/HadeedTariq/webora/pkg/client"
)

const (
	minDepth   = -1
	minWorkers = 1
	maxWorkers = 64
	minDelay   = time.Duration(0)
	minTimeout = time.Second
	maxTimeout = time.Minute * 10
)

type config struct {
	AlowedTags []string
	Ignored    []string
	Include    string
	Regex      *regexp.Regexp
	Client     client.Config
	Delay      time.Duration
	Depth      int
	Robots     RobotsPolicy
	Dirs       DirsPolicy
	Brute      bool
	NoHEAD     bool
	ScanJS     bool
	ScanCSS    bool
	Subdomains bool
}

func (c *config) String() (rv string) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "workers: %d depth: %d timeout: %s", c.Client.Workers, c.Depth, c.Client.Timeout)

	if c.Brute {
		sb.WriteString(" brute: on")
	}

	if c.Delay > 0 {
		fmt.Fprintf(&sb, " delay: %s", c.Delay)
	}

	if c.ScanJS {
		sb.WriteString(" +js")
	}

	if c.ScanCSS {
		sb.WriteString(" +css")
	}

	if c.Subdomains {
		sb.WriteString(" +subdomains")
	}

	if c.Include != "all" {
		fmt.Fprintf(&sb, " include: %s", c.Include)
	}

	return sb.String()
}

func (c *config) validate() {
	c.Client.Workers = min(maxWorkers, max(minWorkers, c.Client.Workers))
	c.Client.Timeout = min(maxTimeout, max(minTimeout, c.Client.Timeout))
	c.Delay = max(minDelay, c.Delay)
	c.Depth = max(minDepth, c.Depth)
}
