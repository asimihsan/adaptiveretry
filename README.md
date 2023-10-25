# adaptiveretry

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

## Contents

This repo will contain Adaptive Retry imlpementations in various languages.
