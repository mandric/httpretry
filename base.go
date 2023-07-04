//
// Flakey HTTP API helper: automatically retry on TCP connection errors and
// support conditional retries.
//
// Error Handling
//
// In this file we should propagate the errors from net/http.  For example,
// non-2xx status codes should not return errors.  Status code is returned so
// callers can implement their own logic.
//
// See https://cs.opensource.google/go/go/+/refs/tags/go1.20.2:src/net/http/client.go;l=434
//
// Singleton Client
//
// Using singleton client to maximize connection pool usage, reuse connections
// and minimize new file handles used.  This improves support for high
// workdloads in resource constrained environments like lambdas.
//
// TODO keep http stats (req/res/code counts) or find library that can

package httpretry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var httpClient *http.Client

func GetSingletonHttpClient() *http.Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return httpClient
}

type RetryPredicate func(resp *http.Response, retryCount int) bool

type httpRequest struct {
	URL              *url.URL
	Token            string
	Header           http.Header
	RetriesMax       int
	RetriesWait      time.Duration
	IsRetryCondition RetryPredicate
}

type HttpRequestOptions struct {
	URL    *url.URL
	Token  string
	Header http.Header

	// RetriesMax max number of retries
	// defaults to 10
	RetriesMax int

	// RetriesWait amount of time to wait between retries
	// defaults to 1sec
	RetriesWait time.Duration

	// IsRetryCondition returns false by default
	//
	// To invoke retries pass in a function that returns true.  Avoid blanket
	// conditions like `return resp.StatusCode != 201` which would retry any time
	// 201 is not encountered.  Minimize retry conditions because they could cause
	// unwanted side effects like:
	//
	//  * slow down tests
	//  * waste resources
	//  * triggering a rate-limit from OAuth if you fail to authenticate and retry
	//    on every test
	//
	// Different HTTP APIs behave differently so work to only specify the edge
	// cases for when a retry has a good chance to succeed.
	IsRetryCondition RetryPredicate
}

func (r httpRequest) doRequest(ctx context.Context, client *http.Client, req *http.Request) (resp *http.Response, respBody []byte, err error) {
	DebugRequest(ctx, req, r.Token)
	resp, err = client.Do(req)
	if err != nil {
		// on error response body can be ignored
		// https://pkg.go.dev/net/http#Client.Do
		return
	}
	defer resp.Body.Close()
	DebugResponse(ctx, resp, r.Token)
	respBody, err = io.ReadAll(resp.Body)
	return
}

// Do retries on request failure (error returned) or if a condition is not met.
// TLS errors for example cause a connection to fail which gets retried.
// For more information on transport layer parameters see:
// https://pkg.go.dev/net/http#Transport.
func (r httpRequest) doRequestWithRetries(ctx context.Context, client *http.Client, req *http.Request) (respBody []byte, statusCode int, err error) {
	var resp *http.Response
	retryCount := 0

	for retryCount < r.RetriesMax {
		retryCount++
		ctx = context.WithValue(ctx, "RequestId", uuid.New().String())
		resp, respBody, err = r.doRequest(ctx, client, req)
		if err != nil {
			logrus.Warnf("Request %p:%s failed. retryCount is %v", req, ctx.Value("RequestId"), retryCount)
		} else {
			if r.IsRetryCondition == nil || r.IsRetryCondition(resp, retryCount) == false {
				return respBody, resp.StatusCode, err
			}
			logrus.Infof("Request %p:%s IsRetryCondition returned true, retryCount is %v", req, ctx.Value("RequestId"), retryCount)
		}
		time.Sleep(r.RetriesWait)
	}

	return respBody, resp.StatusCode, err
}

func NewHttpRequest(options HttpRequestOptions) httpRequest {
	if options.RetriesMax == 0 {
		options.RetriesMax = 10
	}
	if options.RetriesWait == 0 {
		options.RetriesWait = time.Second * 1
	}

	// setting common buildingx headers, don't overwrite caller set options.
	if options.Header == nil {
		options.Header = http.Header{}
	}
	if options.Header.Get("Accept") == "" {
		options.Header.Add("Accept", "application/vnd.api+json")
		options.Header.Add("Accept", "application/json")
		options.Header.Add("Accept", "*/*")
	}
	if options.Header.Get("Content-Type") == "" {
		options.Header.Set("Content-Type", "application/vnd.api+json")
	}
	if options.Header.Get("Authorization") == "" {
		options.Header.Set("Authorization", fmt.Sprintf("Bearer %s", options.Token))
	}

	return httpRequest{
		URL:              options.URL,
		Token:            options.Token,
		Header:           options.Header,
		RetriesMax:       options.RetriesMax,
		RetriesWait:      options.RetriesWait,
		IsRetryCondition: options.IsRetryCondition,
	}
}

func (r httpRequest) HttpGet(ctx context.Context) ([]byte, int, error) {
	client := GetSingletonHttpClient()

	req, err := http.NewRequest(http.MethodGet, r.URL.String(), nil)
	if err != nil {
		return []byte(""), 0, err
	}

	req.Header = r.Header

	return r.doRequestWithRetries(ctx, client, req)
}

func (r httpRequest) HttpPost(ctx context.Context, object []byte) ([]byte, int, error) {
	client := GetSingletonHttpClient()

	req, err := http.NewRequest(http.MethodPost, r.URL.String(), strings.NewReader(string(object)))
	if err != nil {
		return []byte(""), 0, err
	}

	req.Header = r.Header

	return r.doRequestWithRetries(ctx, client, req)
}

func (r httpRequest) HttpPatch(ctx context.Context, object []byte) ([]byte, int, error) {
	client := GetSingletonHttpClient()

	req, err := http.NewRequest(http.MethodPatch, r.URL.String(), strings.NewReader(string(object)))
	if err != nil {
		return []byte(""), 0, err
	}

	req.Header = r.Header

	return r.doRequestWithRetries(ctx, client, req)
}

func (r httpRequest) HttpPut(ctx context.Context, object []byte) ([]byte, int, error) {
	client := GetSingletonHttpClient()

	req, err := http.NewRequest(http.MethodPut, r.URL.String(), bytes.NewBuffer(object))
	if err != nil {
		return []byte(""), 0, err
	}

	req.Header = r.Header

	return r.doRequestWithRetries(ctx, client, req)
}

func (r httpRequest) HttpDelete(ctx context.Context) ([]byte, int, error) {
	client := GetSingletonHttpClient()

	u, err := url.ParseRequestURI(r.URL.String())
	if err != nil {
		return []byte(""), 0, err
	}
	urlStr := u.String()

	req, err := http.NewRequest(http.MethodDelete, urlStr, nil)
	if err != nil {
		return []byte(""), 0, err
	}

	req.Header = r.Header

	return r.doRequestWithRetries(ctx, client, req)
}

func ExtractErrorFromResponse(expectedStatus int, actualStatusCode int, urlCalled *url.URL, responseBody []byte) error {
	return fmt.Errorf("expected %d,\nactual: %d,\nURL: %s,\nresponse: %s", expectedStatus, actualStatusCode, urlCalled.String(), string(responseBody))
}

func DebugRequest(ctx context.Context, req *http.Request, token string) {
	// log := hlogger.Current(ctx)
	reqBytes, err := httputil.DumpRequest(req, true)
	if err != nil {
		logrus.Errorf("DumpRequest failed: %v", err)
		return
	}
	logrus.Debugf("Request %p:%s:\n%s", req, ctx.Value("RequestId"), string(reqBytes))
}

func DebugResponse(ctx context.Context, resp *http.Response, token string) {
	// log := hlogger.Current(ctx)
	respBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		logrus.WithError(err).Errorf("DumpResponse failed")
		return
	}
	logrus.Debugf("Response for %s:\n%s", ctx.Value("RequestId"), string(respBytes))
}
