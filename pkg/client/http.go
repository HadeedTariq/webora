package client

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
)

// HTTP holds pre-configured http.Client.
type HTTP struct {
	c           *http.Client
	ua          string
	extraAgents []string
	cookies     []*http.Cookie
	headers     []*header
	proxies     []*url.URL
}

// New creates and configure client for later use.
func New(cfg *Config) (h *HTTP) {
	var parsedProxies []*url.URL

	for _, pStr := range cfg.Proxies {
		if pStr == "" {
			continue
		}
		pURL, err := url.Parse(pStr)
		if err != nil {
			log.Printf("[-] Warning: skipping invalid proxy URL %s: %v", pStr, err)
			continue
		}
		parsedProxies = append(parsedProxies, pURL)
	}
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if len(parsedProxies) == 0 {
				return nil, nil // No proxy, connect directly
			}
			randomIndex := rand.Intn(len(parsedProxies))
			return parsedProxies[randomIndex], nil
		},
		Dial: (&net.Dialer{
			Timeout: cfg.Timeout,
		}).Dial,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.SkipSSL,
		},
		IdleConnTimeout:     cfg.Timeout,
		TLSHandshakeTimeout: cfg.Timeout,
		MaxConnsPerHost:     cfg.Workers,
		MaxIdleConns:        cfg.Workers,
		MaxIdleConnsPerHost: cfg.Workers,
	}

	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}

	return &HTTP{
		ua:          cfg.UserAgent,
		c:           client,
		extraAgents: cfg.Agents,
		headers:     prepareHeaders(cfg.Headers),
		cookies:     prepareCookies(cfg.Cookies),
	}
}

// Get sends http GET request, returns non-closed body or error.
func (h *HTTP) Get(ctx context.Context, url string) (body io.ReadCloser, hdrs http.Header, statusCode int, err error) {
	var req *http.Request

	if req, err = http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		url,
		http.NoBody,
	); err != nil {
		return
	}

	if body, hdrs, statusCode, err = h.request(req); err != nil {
		return
	}

	return body, hdrs, statusCode, nil
}

// Head sends http HEAD request, return response headers or error.
func (h *HTTP) Head(ctx context.Context, url string) (hdrs http.Header, err error) {
	var req *http.Request

	if req, err = http.NewRequestWithContext(
		ctx,
		http.MethodHead,
		url,
		http.NoBody,
	); err != nil {
		return
	}

	var body io.ReadCloser

	if body, hdrs, _, err = h.request(req); err != nil {
		return
	}

	Discard(body)

	return hdrs, nil
}

// Discard read all contents from ReaderCloser, closing it afterwards.
func Discard(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}

func (h *HTTP) request(req *http.Request) (body io.ReadCloser, hdrs http.Header, statusCode int, err error) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	var userAgent string
	if len(h.extraAgents) > 0 {
		randomIndex := rand.Intn(len(h.extraAgents))
		userAgent = h.extraAgents[randomIndex]
	} else {
		userAgent = h.ua
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", req.URL.String())

	h.enrich(req)

	var resp *http.Response

	if resp, err = h.c.Do(req); err != nil {
		return
	}

	if http.StatusOK < resp.StatusCode && resp.StatusCode >= http.StatusMultipleChoices {
		err = ErrFromResp(resp)
	}

	return resp.Body, resp.Header, resp.StatusCode, err
}

func (h *HTTP) enrich(req *http.Request) {
	for _, hdr := range h.headers {
		req.Header.Set(hdr.Key, hdr.Val)
	}

	for _, cck := range h.cookies {
		req.AddCookie(cck)
	}
}
