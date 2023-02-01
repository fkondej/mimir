// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/thanos-io/thanos/blob/main/pkg/store/limiter.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Thanos Authors.

package storegateway

import (
	"net/http"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/weaveworks/common/httpgrpc"
	"go.uber.org/atomic"
)

type ChunksLimiter interface {
	// Reserve num chunks out of the total number of chunks enforced by the limiter.
	// Returns an error if the limit has been exceeded. This function must be
	// goroutine safe.
	Reserve(num uint64) error

	Log(keyvals ...interface{})
}

type SeriesLimiter interface {
	// Reserve num series out of the total number of series enforced by the limiter.
	// Returns an error if the limit has been exceeded. This function must be
	// goroutine safe.
	Reserve(num uint64) error

	Log(keyvals ...interface{})
}

// ChunksLimiterFactory is used to create a new ChunksLimiter. The factory is useful for
// projects depending on Thanos which have dynamic limits.
type ChunksLimiterFactory func(failedCounter prometheus.Counter) ChunksLimiter

// SeriesLimiterFactory is used to create a new SeriesLimiter.
type SeriesLimiterFactory func(failedCounter prometheus.Counter) SeriesLimiter

// Limiter is a simple mechanism for checking if something has passed a certain threshold.
type Limiter struct {
	limit    uint64
	reserved atomic.Uint64

	// Counter metric which we will increase if limit is exceeded.
	failedCounter prometheus.Counter
	failedOnce    sync.Once
	logger        log.Logger
}

// NewLimiter returns a new limiter with a specified limit. 0 disables the limit.
func NewLimiter(limit uint64, ctr prometheus.Counter, logger log.Logger) *Limiter {
	return &Limiter{limit: limit, failedCounter: ctr, logger: logger}
}

// Reserve implements ChunksLimiter.
func (l *Limiter) Reserve(num uint64) error {
	if l.limit == 0 {
		return nil
	}
	if reserved := l.reserved.Add(num); reserved > l.limit {
		// We need to protect from the counter being incremented twice due to concurrency
		// while calling Reserve().
		l.failedOnce.Do(l.failedCounter.Inc)
		if l.logger != nil {
			level.Warn(l.logger).Log("source", "limiter.go", "func", "Reserve", "msg", "Adding "+string(num)+" elements to the limiter", "reserved", l.reserved.Load(), "limit", l.limit, "status", "FAILED")
		}
		return httpgrpc.Errorf(http.StatusUnprocessableEntity, "limit %v exceeded", l.limit)
	}
	if num != 0 && l.logger != nil {
		level.Warn(l.logger).Log("source", "limiter.go", "func", "Reserve", "msg", "Adding "+string(num)+" elements to the limiter", "reserved", l.reserved.Load(), "limit", l.limit, "status", "OK")
	}
	return nil
}

func (l *Limiter) Log(keyvals ...interface{}) {
	level.Warn(l.logger).Log(keyvals)
}

// NewChunksLimiterFactory makes a new ChunksLimiterFactory with a dynamic limit.
func NewChunksLimiterFactory(limitsExtractor func() uint64, logger log.Logger) ChunksLimiterFactory {
	return func(failedCounter prometheus.Counter) ChunksLimiter {
		return NewLimiter(limitsExtractor(), failedCounter, logger)
	}
}

// NewSeriesLimiterFactory makes a new NewSeriesLimiterFactory with a dynamic limit.
func NewSeriesLimiterFactory(limitsExtractor func() uint64, logger log.Logger) SeriesLimiterFactory {
	return func(failedCounter prometheus.Counter) SeriesLimiter {
		return NewLimiter(limitsExtractor(), failedCounter, logger)
	}
}
