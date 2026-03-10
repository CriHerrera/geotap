package scraper

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/rendis/geotap/internal/model"
)

const (
	searchBaseURL = "https://www.google.com/search"

	maxRetries   = 3
	baseBackoff  = 2 * time.Second
	maxBackoff   = 30 * time.Second
	jitterFactor = 0.5
)

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
}

// RateLimitError indicates Google is rate limiting us.
type RateLimitError struct {
	StatusCode int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (status %d)", e.StatusCode)
}

type Client struct {
	http       *http.Client
	lang       string
	zoom       int
	rateLimits atomic.Int64
}

func NewClient(lang, proxyURL string, zoom int) *Client {
	jar, _ := cookiejar.New(nil)
	googleURL, _ := url.Parse("https://www.google.com")
	jar.SetCookies(googleURL, []*http.Cookie{
		{Name: "CONSENT", Value: "YES+ES.es+V14+BX", Path: "/", Domain: ".google.com"},
	})

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}

			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}

			// Get Chrome TLS spec and force HTTP/1.1 ALPN
			spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
			if err != nil {
				conn.Close()
				return nil, err
			}
			for i, ext := range spec.Extensions {
				if alpn, ok := ext.(*utls.ALPNExtension); ok {
					alpn.AlpnProtocols = []string{"http/1.1"}
					spec.Extensions[i] = alpn
					break
				}
			}

			tlsConn := utls.UClient(conn, &utls.Config{
				ServerName: host,
			}, utls.HelloCustom)
			if err := tlsConn.ApplyPreset(&spec); err != nil {
				conn.Close()
				return nil, err
			}
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}

			return tlsConn, nil
		},
		MaxIdleConns:        150,
		MaxIdleConnsPerHost: 150,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	}

	if proxyURL != "" {
		proxyParsed, err := url.Parse(proxyURL)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyParsed)
			// When using proxy, fall back to standard TLS (proxy handles connection)
			transport.DialTLSContext = nil
			transport.TLSClientConfig = &tls.Config{}
		}
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			Jar:       jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		lang: lang,
		zoom: zoom,
	}
}

// SearchMap performs a Maps search (tbm=map) with retry and exponential backoff.
func (c *Client) SearchMap(sector model.Sector, query string, offset int) ([]byte, error) {
	pb := BuildPB(sector.Lat, sector.Lng, c.zoom, offset)

	params := url.Values{}
	params.Set("tbm", "map")
	params.Set("authuser", "0")
	params.Set("hl", c.lang)
	params.Set("q", query)
	params.Set("pb", pb)

	reqURL := searchBaseURL + "?" + params.Encode()

	var lastErr error
	for attempt := range maxRetries {
		body, err := c.doRequest(reqURL)
		if err == nil {
			c.rateLimits.Store(0)
			return body, nil
		}

		lastErr = err

		if _, ok := err.(*RateLimitError); !ok {
			return nil, err
		}

		c.rateLimits.Add(1)

		backoff := baseBackoff * time.Duration(1<<uint(attempt))
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		jitter := time.Duration(float64(backoff) * jitterFactor * rand.Float64())
		time.Sleep(backoff + jitter)
	}

	return nil, lastErr
}

// ConsecutiveRateLimits returns how many consecutive rate limits have occurred across all workers.
func (c *Client) ConsecutiveRateLimits() int64 {
	return c.rateLimits.Load()
}

// FetchPlace fetches the Google Maps place detail page for a given Place ID.
// Returns the raw HTML body which contains embedded JSON with photo URLs.
func (c *Client) FetchPlace(placeID string) ([]byte, error) {
	reqURL := "https://www.google.com/maps/place/?q=place_id:" + placeID + "&hl=" + c.lang
	return c.doRequestWithRetry(reqURL)
}

// doRequestWithRetry wraps doRequest with the same retry logic as SearchMap.
func (c *Client) doRequestWithRetry(reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := range maxRetries {
		body, err := c.doRequest(reqURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if _, ok := err.(*RateLimitError); !ok {
			return nil, err
		}
		backoff := baseBackoff * time.Duration(1<<uint(attempt))
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		jitter := time.Duration(float64(backoff) * jitterFactor * rand.Float64())
		time.Sleep(backoff + jitter)
	}
	return nil, lastErr
}

func (c *Client) doRequest(reqURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgents[rand.IntN(len(userAgents))])
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Referer", "https://www.google.com/")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusFound,
		resp.StatusCode == http.StatusMovedPermanently,
		resp.StatusCode == http.StatusTemporaryRedirect:
		io.Copy(io.Discard, resp.Body)
		return nil, &RateLimitError{StatusCode: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	return body, nil
}
