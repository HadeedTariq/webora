package client

import "time"

type Config struct {
	UserAgent string
	Headers   []string
	Cookies   []string
	Proxies   []string
	Agents    []string
	Workers   int
	Timeout   time.Duration
	SkipSSL   bool
}
