package httpretry

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	// "github.com/avast/retry-go/v4"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_HttpPost(t *testing.T) {

	logrus.SetFormatter(&logrus.TextFormatter{
		DisableQuote: true,
	})
	logrus.SetLevel(logrus.DebugLevel)

	t.Run("GIVEN a server that returns 429 for 5 requests AND the request body", func(t *testing.T) {
		attempts := 5

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := ioutil.ReadAll(r.Body)
			require.NoError(t, err)
			if attempts > 0 {
				w.WriteHeader(http.StatusTooManyRequests)
				attempts--
			} else {
				w.WriteHeader(http.StatusOK)
			}
			w.Write(body)
		}))
		defer ts.Close()

		t.Run("AND http request with retry condition is set", func(t *testing.T) {

			url, err := url.Parse(ts.URL)
			require.NoError(t, err)

			api := NewHttpRequest(HttpRequestOptions{
				URL: url,
				IsRetryCondition: func(resp *http.Response, retryCount int) bool {
					return resp.StatusCode != http.StatusOK
				},
			})

			t.Run("WHEN HttpPost request with unique body is sent", func(t *testing.T) {
				requestBody := uuid.New().String()
				result, code, err := api.HttpPost(context.Background(), []byte(requestBody))
				require.NoError(t, err)

				t.Run("THEN request body is in response", func(t *testing.T) {
					assert.Equal(t, http.StatusOK, code)
					assert.Equal(t, requestBody, string(result))
				})

			})
		})
	})
}
