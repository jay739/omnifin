package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type updaterTestTransport func(*http.Request) (*http.Response, error)

func (f updaterTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type updaterTestBody struct {
	io.Reader
	closed bool
}

func (b *updaterTestBody) Close() error {
	b.closed = true
	return nil
}

func TestUpdaterHTTPFailures(t *testing.T) {
	for _, method := range []string{"tag", "release"} {
		t.Run(method, func(t *testing.T) {
			for _, scenario := range []string{"network error", "non-success response"} {
				t.Run(scenario, func(t *testing.T) {
					transportErr := errors.New("connection unavailable")
					body := &updaterTestBody{Reader: strings.NewReader("service unavailable")}
					timeoutCalls := 0
					ud := &Updater{
						buildType:      external,
						url:            "https://updates.example.invalid",
						timeoutHandler: func() { timeoutCalls++ },
						httpClient: &http.Client{Transport: updaterTestTransport(func(*http.Request) (*http.Response, error) {
							if scenario == "network error" {
								return nil, transportErr
							}
							return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: body}, nil
						})},
					}
					var status int
					var err error
					if method == "tag" {
						_, status, err = ud.GetTag()
					} else {
						_, status, err = ud.getRelease()
					}
					if scenario == "network error" {
						if !errors.Is(err, transportErr) || status != -1 {
							t.Fatalf("got status %d, error %v; want transport error with status -1", status, err)
						}
					} else {
						if status != http.StatusServiceUnavailable || err == nil {
							t.Fatalf("got status %d, error %v; want explicit HTTP failure", status, err)
						}
						if !body.closed {
							t.Fatal("unsuccessful response body was not closed")
						}
					}
					if timeoutCalls != 1 {
						t.Fatalf("timeout handler called %d times, want 1", timeoutCalls)
					}
				})
			}
		})
	}
}
