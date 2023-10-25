/*
 * Copyright 2023 Asim Ihsan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package retry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryer_Do(t *testing.T) {
	tests := []struct {
		name                string
		maxAttempts         int
		serverResponses     []int
		expectedServerCalls int
		wantErr             bool
	}{
		{
			name:                "Success",
			maxAttempts:         1,
			serverResponses:     []int{http.StatusOK},
			expectedServerCalls: 1,
			wantErr:             false,
		},
		{
			name:                "Success after 1 failure",
			maxAttempts:         2,
			serverResponses:     []int{http.StatusInternalServerError, http.StatusOK},
			expectedServerCalls: 2,
			wantErr:             false,
		},
		{
			name:                "Success after 2 failures",
			maxAttempts:         3,
			serverResponses:     []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK},
			expectedServerCalls: 3,
			wantErr:             false,
		},
		{
			name:                "Failure with max attempts set to 1",
			maxAttempts:         1,
			serverResponses:     []int{http.StatusInternalServerError},
			expectedServerCalls: 1,
			wantErr:             true,
		},
		{
			name:                "Two failures with max attempts set to 2",
			maxAttempts:         2,
			serverResponses:     []int{http.StatusInternalServerError, http.StatusInternalServerError},
			expectedServerCalls: 2,
			wantErr:             true,
		},
		{
			name:                "Non-retryable error",
			maxAttempts:         3,
			serverResponses:     []int{http.StatusBadRequest},
			expectedServerCalls: 1,
			wantErr:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.serverResponses[attempts])
				attempts++
			}))
			defer server.Close()

			var sleepTimes []time.Duration
			sleep := func(d time.Duration) {
				sleepTimes = append(sleepTimes, d)
			}

			config := Config{
				MaxAttempts: tt.maxAttempts,
				MaxBackoff:  DefaultMaxBackoff,
			}

			retryer := NewRetryer(
				config,
				WithSleep(sleep),
			)

			err := retryer.Do(context.Background(), func(ctx context.Context) error {
				resp, err := http.Get(server.URL)
				defer func() {
					if resp != nil {
						err := resp.Body.Close()
						assert.NoError(t, err)
					}
				}()

				if err != nil {
					return err
				}
				if resp.StatusCode != http.StatusOK {
					return errors.New("server error")
				}
				return nil
			})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedServerCalls, attempts)

			if tt.maxAttempts > 1 {
				assert.Greater(t, len(sleepTimes), 0)
				for i := 1; i < len(sleepTimes); i++ {
					assert.GreaterOrEqual(t, sleepTimes[i], sleepTimes[i-1])
				}
			} else {
				assert.Equal(t, 0, len(sleepTimes))
			}
		})
	}
}

func TestRetryer_Do_WithRateLimitFirstRequestEnabled(t *testing.T) {
	// atomic variable holding server response to make
	var serverResponse int32 = http.StatusInternalServerError

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(atomic.LoadInt32(&serverResponse)))
	}))
	defer server.Close()

	var sleepTimes []time.Duration
	sleep := func(d time.Duration) {
		sleepTimes = append(sleepTimes, d)
	}

	config := NewDefaultConfig()

	retryer := NewRetryer(
		config,
		WithSleep(sleep),
		WithRateLimitFirstRequestEnabled(),
	)

	// Exhaust the token bucket. Recall that the capacity is DefaultRetryRateTokens/DefaultRetryCost. But each
	// iteration we make config.MaxAttempts calls. And then we add one to make sure we exhaust the bucket.
	iterations := int(DefaultRetryRateTokens / DefaultRetryCost)
	iterations /= config.MaxAttempts
	iterations++

	for i := 0; i < iterations; i++ {
		err := retryer.Do(context.Background(), func(ctx context.Context) error {
			resp, err := http.Get(server.URL)
			defer func() {
				if resp != nil {
					err := resp.Body.Close()
					assert.NoError(t, err)
				}
			}()
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return errors.New("server error")
			}
			return nil
		})
		assert.Error(t, err)
	}

	// Change the server response to success
	atomic.StoreInt32(&serverResponse, http.StatusOK)

	// Make one more request. Despite the retry token bucket being both exhausted and being used to rate limit
	// the first attempt, the request should still be successful because we allow probes to be made.
	err := retryer.Do(context.Background(), func(ctx context.Context) error {
		resp, err := http.Get(server.URL)
		defer func() {
			if resp != nil {
				err := resp.Body.Close()
				assert.NoError(t, err)
			}
		}()
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return errors.New("server error")
		}
		return nil
	})

	// The request should be successful even though the token bucket is exhausted
	assert.NoError(t, err)
}

// TestRetryer_Do_ContextCancelled tests that the retry operation is aborted when the context is cancelled.
// Even though by default we do MaxAttempts (3) retries, in this case, we should not do any retries because
// the context is done. The test server is set up to confirm that it is only called once.
func TestRetryer_Do_ContextCancelled(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	config := NewDefaultConfig()

	retryer := NewRetryer(config)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediately cancel the context

	err := retryer.Do(ctx, func(ctx context.Context) error {
		resp, err := http.Get(server.URL)
		defer func() {
			if resp != nil {
				err := resp.Body.Close()
				assert.NoError(t, err)
			}
		}()
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return errors.New("server error")
		}
		return nil
	})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, 1, called)
}

// TestRetryer_Do_ContextDeadlineExceeded tests that the retry operation is aborted when the context deadline is exceeded.
// Even though by default we do MaxAttempts (3) retries, in this case, we should not do any retries because
// the context is done. The test server is set up to confirm that it is only called once.
func TestRetryer_Do_ContextDeadlineExceeded(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	config := NewDefaultConfig()

	retryer := NewRetryer(config)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel() // Cleanup the context when we're done

	err := retryer.Do(ctx, func(ctx context.Context) error {
		resp, err := http.Get(server.URL)
		defer func() {
			if resp != nil {
				err := resp.Body.Close()
				assert.NoError(t, err)
			}
		}()
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return errors.New("server error")
		}
		return nil
	})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Equal(t, 1, called)
}
