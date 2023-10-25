<h1 align="center">
  Adaptive Retry
</h1>

<p align="center">
  <a href="#overview">Overview</a> •
  <a href="#installation">Installation</a> •
  <a href="#usage">Usage</a> •
  <a href="#design">Design</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#testing">Testing</a> •
  <a href="#contributing">Contributing</a>
</p>

## Overview

Adaptive Retry is a flexible, configurable retry mechanism with a focus on safety and preventing retry amplification in
distributed systems. It is designed to allow arbitrary functions to be retried until they succeed or a maximum number of
attempts is reached.

The problem of retry amplification in distributed systems is a significant one. When a service is under high load or
experiencing partial outages, aggressive retry strategies can exacerbate the problem, leading to a cascade of failures.
This is where Adaptive Retry comes in. It uses a token bucket approach to limit retries, inspired by the AWS SDK,
preventing retry amplification and helping to maintain system stability.

Unlike other retry or circuit breaker packages, Adaptive Retry is designed with lessons learned from operating
large-scale distributed systems. While libraries like hashicorp/go-retryablehttp offer retry mechanisms, they often lack
sophisticated controls to prevent retry amplification, and they are typically specific to HTTP clients. On the other
hand, Adaptive Retry is not tied to any specific protocol and incorporates a token bucket approach to enforce request
quotas.

## Installation

To install Adaptive Retry, you can use go get:

```bash
go get github.com/adaptiveretry/go
```

## Usage

To use the Adaptive Retry package, create a Retryer with the desired configuration and call its Do method with the
function you want to retry:

```go
package main

import (
	"context"
    "github.com/asimihsan/adaptiveretry/go"
)

retryer := retry.NewRetryer(retry.NewDefaultConfig())
ctx := context.Background()

err := retryer.Do(ctx, func (ctx context.Context) error {
    // Your code here.
})
```

If the function succeeds (i.e., returns nil), then Do also returns nil. If the function fails (i.e., returns an error)
and the maximum number of attempts is reached, then Do returns an error.

## Design

Adaptive Retry uses a token bucket to limit retries. Once the token bucket is exhausted, no more retries are allowed,
but calls without retries can still go through. This approach prevents retry amplification and helps to maintain system
stability during periods of high load or partial outages.

## Configuration

The behavior of the Retryer can be customized by providing a Config struct when creating it. The Config struct includes
options for the maximum number of attempts, the maximum backoff delay, the set of retryable checks, the set of timeout
checks, and various parameters for the token bucket.

## Testing

The package includes a comprehensive set of tests to ensure its functionality and reliability. You can run the tests
using the go test command:

```bash
go test github.com/adaptiveretry/go
```

## Contributing

Contributions to Adaptive Retry are welcome! Please submit a pull request or create an issue on GitHub. We appreciate
your help in making this library better.

For more details, see the documentation for the Retryer, Config, and related types.
