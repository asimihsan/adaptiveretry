/*
Package retry provides a flexible, configurable retry mechanism with a focus on safety and preventing retry
amplification in distributed systems. It is designed to allow arbitrary functions to be retried until they
succeed or a maximum number of attempts is reached.

The main type in this package is Retryer, which encapsulates the retry logic. The primary method on Retryer is
Do, which takes a function and attempts to execute it until it succeeds or the maximum number of attempts is
reached.

Usage:

To use the retry package, create a Retryer with the desired configuration and call its Do method with the
function you want to retry:

	retryer := retry.NewRetryer(retry.NewDefaultConfig())
	err := retryer.Do(ctx, func(ctx context.Context) error {
	    // Your code here.
	})

If the function succeeds (i.e., returns nil), then Do also returns nil. If the function fails (i.e., returns
an error) and the maximum number of attempts is reached, then Do returns an error.

Design:

The retry package uses a token bucket to limit retries. Once the token bucket is exhausted, no more retries
are allowed, but calls without retries can still go through. This approach, inspired by the AWS SDK, prevents
retry amplification and helps to maintain system stability during periods of high load or partial outages.

Algorithm details when configured with default RateLimitFirstRequestDisabled:

 1. When a function is passed to the Do method of a Retryer, the function is executed.
 2. If the function succeeds (returns nil), the Do method also returns nil. The token bucket is then
    incremented by a fixed amount (the NoRetryIncrement), rewarding the successful operation.
 3. If the function fails (returns an error), the Retryer checks if the maximum number of attempts has been
    reached. If so, it returns an error.
 4. If the maximum number of attempts has not been reached, the Retryer attempts to get a retry token from the
    token bucket, deducting a certain cost (the RetryCost or RetryTimeoutCost, depending on the type of error).
 5. If a retry token is successfully obtained, the function is retried. If the retry is successful, the cost of
    the retry token is refunded to the token bucket.
 6. The process repeats until the function succeeds or the maximum number of attempts is reached.

The retry package also includes an optional circuit breaker mode, which can be enabled by setting the
RateLimitFirstRequest option to RateLimitFirstRequestEnabled when creating a Retryer. In this mode, the
Retryer will start rate limiting the first request attempt once the token bucket is exhausted, effectively
opening the circuit breaker.

Unlike other retry or circuit breaker packages, the retry package is designed with lessons learned from
operating large-scale distributed systems. While libraries like hashicorp/go-retryablehttp offer retry
mechanisms, they often lack sophisticated controls to prevent retry amplification, and they are typically
specific to HTTP clients. On the other hand, our retry package is not tied to any specific protocol and
incorporates a token bucket approach to enforce request quotas, a strategy inspired by the AWS SDK, which
itself is based on over two decades of experience operating services and providing SDKs to customers at AWS.

In contrast to the Google SRE book's method of retry budgets, which requires coordination between different
parts of the system, our retry package provides a self-contained mechanism for safe retries. This makes it
easier to use and integrate into your system without needing to coordinate with other teams or services.

Furthermore, while libraries like sony/gobreaker provide basic circuit breaker functionality, they often use
simple failure ratios to open circuit breakers. Our retry package, however, offers an optional circuit breaker
mode that is more sophisticated. It starts rate limiting the first request attempt once the token bucket is
exhausted, effectively opening the circuit breaker.

Configuration:

The behavior of the Retryer can be customized by providing a Config struct when creating it. The Config struct
includes options for the maximum number of attempts, the maximum backoff delay, the set of retryable checks,
the set of timeout checks, and various parameters for the token bucket.

For more details, see the documentation for the Retryer, Config, and related types.
*/
package retry

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsratelimit "github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	awsretry "github.com/aws/aws-sdk-go-v2/aws/retry"
	connect_go "github.com/bufbuild/connect-go"
	"github.com/rs/zerolog"
)

const (
	// DefaultMaxAttempts is the maximum of attempts for a request.
	DefaultMaxAttempts int = 3

	// DefaultMaxBackoff is the maximum back off delay between attempts.
	DefaultMaxBackoff = time.Second
)

// def retry token quota values.
const (
	// DefaultRetryRateTokens is the number of tokens in the token bucket for the retryRateLimiter.
	//
	// With the defaults, you get 100 failed retries before you are rate limited, or 50 failed retries due to
	// timeouts before you are rate limited.
	DefaultRetryRateTokens uint = 500

	// DefaultRetryCost is the cost of a single failed retry attempt. If you retry, and you succeed, you get a refund.
	// But if the retry fails you lose the tokens.
	DefaultRetryCost uint = 5

	// DefaultRetryTimeoutCost is the cost of a single failed retry attempt due to a timeout error.
	// If you retry and you succeed, you get a refund. But if the retry fails you lose the tokens.
	DefaultRetryTimeoutCost uint = 10

	// DefaultNoRetryIncrement is the number of tokens to add to the token bucket for a successful attempt.
	DefaultNoRetryIncrement uint = 1

	// DefaultProbeRateLimit is the calls per second to allow if the retry token bucket is exhausted and it is
	// also being used to rate limit the first attempt.
	DefaultProbeRateLimit uint = 1
)

type Config struct {
	// Maximum number of attempts that should be made.
	MaxAttempts int

	// MaxBackoff duration between retried attempts.
	MaxBackoff time.Duration

	// Retryables is the set of retryable checks that should be used.
	Retryables awsretry.IsErrorRetryables

	// Timeouts is the set of timeout checks that should be used.
	Timeouts awsretry.IsErrorTimeouts

	// RetryRateTokens is the number of tokens in the token bucket for the retryRateLimiter.
	RetryRateTokens uint

	// The cost to deduct from the retryRateLimiter's token bucket per retry.
	RetryCost uint

	// The cost to deduct from the retryRateLimiter's token bucket per retry caused
	// by timeout error.
	RetryTimeoutCost uint

	// The cost to payback to the retryRateLimiter's token bucket for successful
	// attempts.
	NoRetryIncrement uint

	// ProbeRateLimit is the calls per second to allow if the retry token bucket is exhausted and it is
	// also being used to rate limit the first attempt. This is used as the max and refill rate for the
	// probeRateLimiter.
	ProbeRateLimit uint
}

var defaultConfig = Config{
	MaxAttempts:      DefaultMaxAttempts,
	MaxBackoff:       DefaultMaxBackoff,
	Retryables:       awsretry.IsErrorRetryables(append([]awsretry.IsErrorRetryable{}, DefaultRetryables...)),
	Timeouts:         awsretry.IsErrorTimeouts(append([]awsretry.IsErrorTimeout{}, DefaultTimeouts...)),
	RetryRateTokens:  DefaultRetryRateTokens,
	RetryCost:        DefaultRetryCost,
	RetryTimeoutCost: DefaultRetryTimeoutCost,
	NoRetryIncrement: DefaultNoRetryIncrement,
	ProbeRateLimit:   DefaultProbeRateLimit,
}

func NewDefaultConfig() Config {
	return defaultConfig
}

// defaultRetryableHTTPStatusCodes is the default set of HTTP status codes we should consider as retryable errors.
var defaultRetryableHTTPStatusCodes = map[int]struct{}{
	500: {},
	502: {},
	503: {},
	504: {},
}

// defaultThrottleHTTPStatusCodes is the default set of HTTP status codes we should consider as throttle errors.
var defaultThrottleHTTPStatusCodes = map[int]struct{}{
	429: {},
}

// defaultRetryableConnectErrorCodes provides the set of Connect error codes [1] that should be retried.
//
// [1] https://connectrpc.com/docs/protocol#error-codes
var defaultRetryableConnectErrorCodes = map[connect_go.Code]struct{}{
	connect_go.CodeCanceled:    {},
	connect_go.CodeInternal:    {},
	connect_go.CodeUnavailable: {},

	// Currently MDU Python services we call do not return informative Connect error codes. For now assume that
	// all unknown errors are retryable.
	connect_go.CodeUnknown: {},
}

// defaultTimeoutConnectErrorCodes provides the set of Connect error codes [1] that should be retried.
//
// [1] https://connectrpc.com/docs/protocol#error-codes
var defaultTimeoutConnectErrorCodes = map[connect_go.Code]struct{}{
	connect_go.CodeDeadlineExceeded: {},
}

// defaultThrottleConnectErrorCodes provides the set of Connect error codes [1] that are considered throttle errors.
//
// [1] https://connectrpc.com/docs/protocol#error-codes
var defaultThrottleConnectErrorCodes = map[connect_go.Code]struct{}{
	connect_go.CodeResourceExhausted: {},
}

// DefaultRetryables provides the set of retryable checks that are used by
// default.
var DefaultRetryables = []awsretry.IsErrorRetryable{
	// RetryableConnectionError determines if the underlying error is an HTTP
	// connection and returns if it should be retried.
	//
	// Includes errors such as connection reset, connection refused, net dial,
	// temporary, and timeout errors.
	awsretry.RetryableConnectionError{},

	awsretry.RetryableHTTPStatusCode{
		Codes: defaultRetryableHTTPStatusCodes,
	},
	awsretry.RetryableHTTPStatusCode{
		Codes: defaultThrottleHTTPStatusCodes,
	},
	RetryableConnectErrorCode{
		Codes: defaultRetryableConnectErrorCodes,
	},
	RetryableConnectErrorCode{
		Codes: defaultThrottleConnectErrorCodes,
	},
	RetryableConnectErrorCode{
		Codes: defaultTimeoutConnectErrorCodes,
	},
}

var DefaultTimeouts = []awsretry.IsErrorTimeout{
	awsretry.TimeouterError{},
}

// RetryableConnectErrorCode determines if an attempt should be retried based on the
// Connect error code [1].
//
// [1] https://connectrpc.com/docs/protocol#error-codes
type RetryableConnectErrorCode struct {
	Codes map[connect_go.Code]struct{}
}

// IsErrorRetryable return if the error is retryable based on the Connect error code.
func (r RetryableConnectErrorCode) IsErrorRetryable(err error) aws.Ternary {
	code := connect_go.CodeOf(err)

	_, ok := r.Codes[code]
	if !ok {
		return aws.UnknownTernary
	}

	return aws.TrueTernary
}

type Retryer struct {
	config Config

	// Provides the rate limiting strategy for rate limiting attempt retries
	// across all attempts the retryer is being used with.
	//
	// Note: first attempt is not rate limited.
	retryRateLimiter *awsratelimit.TokenRateLimit

	backoff awsretry.BackoffDelayer

	// sleep is used to sleep between retries. This is exposed for testing.
	sleep func(delay time.Duration)

	// rateLimitFirstRequestEnabled determines whether rate limiting should be applied to the first request attempt.
	// by default, this is set to false.
	//
	// If set to true, rate limiting is applied, following a traditional circuit breaker pattern.
	// If set to false (the default), rate limiting is not applied to the first request attempt. This is mor
	// conservative  and mitigates the amplification effects of retries across a stack, while still allowing for calls
	// to always go through on the first attempt.
	rateLimitFirstRequestEnabled bool

	probeRateLimiter *awsratelimit.TokenRateLimit

	logger zerolog.Logger
}

type RetryerOption func(*Retryer)

func NewRetryer(
	config Config,
	opts ...RetryerOption,
) *Retryer {
	r := &Retryer{
		config:                       config,
		retryRateLimiter:             awsratelimit.NewTokenRateLimit(config.RetryRateTokens),
		backoff:                      awsretry.NewExponentialJitterBackoff(config.MaxBackoff),
		rateLimitFirstRequestEnabled: false,
		sleep:                        time.Sleep,
		probeRateLimiter:             awsratelimit.NewTokenRateLimit(config.ProbeRateLimit),
		logger:                       zerolog.Nop(),
	}

	for _, opt := range opts {
		opt(r)
	}

	if r.rateLimitFirstRequestEnabled {
		if r.config.ProbeRateLimit == 0 {
			r.config.ProbeRateLimit = 1 // Set a default value of one call per second.
		}

		// Start a ticker to add config.ProbeRateLimit to probeRateLimiter at a rate of config.ProbeRateLimit.
		go func() {
			ticker := time.NewTicker(time.Second / time.Duration(r.config.ProbeRateLimit))
			defer ticker.Stop()

			for range ticker.C {
				_ = r.probeRateLimiter.AddTokens(config.ProbeRateLimit)
			}
		}()
	}

	return r
}

func WithRateLimitFirstRequestEnabled() RetryerOption {
	return func(r *Retryer) {
		r.rateLimitFirstRequestEnabled = true
	}
}

func WithRateLimitFirstRequestDisabled() RetryerOption {
	return func(r *Retryer) {
		r.rateLimitFirstRequestEnabled = false
	}
}

// WithSleep sets the sleep function used to sleep between retries. This is exposed for testing.
func WithSleep(sleep func(time.Duration)) RetryerOption {
	return func(r *Retryer) {
		r.sleep = sleep
	}
}

func WithLogger(logger zerolog.Logger) RetryerOption {
	return func(r *Retryer) {
		r.logger = logger
	}
}

// GetRetryToken attempts to deduct the retry cost from the retry token pool. Returns the token release function,
// or error.
//
// If isProbe is true, then this is a probe request and we are treating the token bucket as a circuit breaker.
// In this case, we are allowed to make a request despite the retry token bucket being exhausted, because we need
// to allow through a small, safe rate of traffic to determine when it is safe to resume normal traffic.
func (r *Retryer) GetRetryToken(ctx context.Context, opErr error, isProbe bool) (func(error) error, error) {

	cost := r.config.RetryCost

	// Timeouts are more expensive to retry. The theory is that if a timeout is reached, not only did we hold open
	// a connection for longer, but perhaps we also held open a resource on the server for longer or this resource
	// is under contention. Therefore, we should be more conservative with retries.
	if r.config.Timeouts.IsErrorTimeout(opErr).Bool() {
		cost = r.config.RetryTimeoutCost
	}

	fn, err := r.retryRateLimiter.GetToken(ctx, cost)
	if err == nil {
		return releaseToken(fn).release, nil
	}

	if !isProbe {
		return nil, fmt.Errorf("failed to get retry rate limit token, %w", err)
	}

	// If this is a probe and the token bucket is exhausted, we are treating the token bucket as a
	// circuit breaker and we need to allow through a small, safe rate of traffic to determine when
	// it is safe to resume normal traffic.
	_, err = r.probeRateLimiter.GetToken(ctx, r.config.ProbeRateLimit)
	if err == nil {
		// If we were able to get a token from the probe rate limiter, then we are allowed to make a
		// request despite the retry token bucket being exhausted.
		return nopReleaseToken, nil
	}

	return nil, fmt.Errorf("failed to get rate limit token after probe rate check, %w", err)
}

// noRetryIncrement adds the NoRetryIncrement to the RateLimiter token pool. This is how we reward successful attempts.
func (r *Retryer) noRetryIncrement() error {
	return r.retryRateLimiter.AddTokens(r.config.NoRetryIncrement)
}

// Do will attempt to execute the provided function until it succeeds, or the max attempts is reached.
func (r *Retryer) Do(ctx context.Context, f func(context.Context) error) error {
	var attempts int
	for {
		err := func() error {
			var release func(error) error
			var err error

			if r.rateLimitFirstRequestEnabled || attempts > 0 {
				isProbe := r.rateLimitFirstRequestEnabled && attempts == 0

				release, err = r.GetRetryToken(ctx, err, isProbe)
				if err != nil {
					return fmt.Errorf("failed to get retry token, isProbe: %t, %w", isProbe, err)
				}
			}

			err = f(ctx)

			if err == nil {
				// Successful attempt, deliver the constant reward to the rate limiter.
				if errIncrement := r.noRetryIncrement(); errIncrement != nil {
					r.logger.Warn().Err(errIncrement).Msg("failed to increment retry rate limiter")
				}

				// Release the retry token. This refunds the token cost if the attempt was a retry and it was
				// successful.
				if release != nil {
					_ = release(err)
				}
			}

			return err
		}()

		if err == nil {
			return nil
		}

		attempts++
		if attempts >= r.config.MaxAttempts {
			// Failing all retries means you do not get any refunds back to the token bucket.
			return fmt.Errorf("max retry attempts reached: %w", err)
		}

		delay, _ := r.backoff.BackoffDelay(attempts, err)
		r.sleep(delay)
	}
}

type releaseToken func() error

func (f releaseToken) release(err error) error {
	if err != nil {
		return nil //nolint:nilerr
	}

	return f()
}

func nopReleaseToken(error) error { return nil }
