// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testtrace

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/api/kv"
	"go.opentelemetry.io/otel/api/trace"
)

var _ trace.Tracer = (*Tracer)(nil)

// Tracer is an OpenTelemetry Tracer implementation used for testing.
type Tracer struct {
	// Name is the instrumentation name.
	Name string
	// Version is the instrumentation version.
	Version string

	config *config
}

func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.StartOption) (context.Context, trace.Span) {
	var c trace.StartConfig
	for _, opt := range opts {
		opt(&c)
	}

	startTime := time.Now()
	if st := c.StartTime; !st.IsZero() {
		startTime = st
	}

	span := &Span{
		tracer:     t,
		startTime:  startTime,
		attributes: make(map[kv.Key]kv.Value),
		links:      make(map[trace.SpanContext][]kv.KeyValue),
		spanKind:   c.SpanKind,
	}

	if c.NewRoot {
		span.spanContext = trace.EmptySpanContext()

		iodKey := kv.Key("ignored-on-demand")
		if lsc := trace.SpanFromContext(ctx).SpanContext(); lsc.IsValid() {
			span.links[lsc] = []kv.KeyValue{iodKey.String("current")}
		}
		if rsc := trace.RemoteSpanContextFromContext(ctx); rsc.IsValid() {
			span.links[rsc] = []kv.KeyValue{iodKey.String("remote")}
		}
	} else {
		span.spanContext = t.config.SpanContextFunc(ctx)
		if lsc := trace.SpanFromContext(ctx).SpanContext(); lsc.IsValid() {
			span.spanContext.TraceID = lsc.TraceID
			span.parentSpanID = lsc.SpanID
		} else if rsc := trace.RemoteSpanContextFromContext(ctx); rsc.IsValid() {
			span.spanContext.TraceID = rsc.TraceID
			span.parentSpanID = rsc.SpanID
		}
	}

	for _, link := range c.Links {
		span.links[link.SpanContext] = link.Attributes
	}

	span.SetName(name)
	span.SetAttributes(c.Attributes...)

	if t.config.SpanRecorder != nil {
		t.config.SpanRecorder.OnStart(span)
	}
	return trace.ContextWithSpan(ctx, span), span
}

func (t *Tracer) WithSpan(ctx context.Context, name string, body func(ctx context.Context) error, opts ...trace.StartOption) error {
	ctx, span := t.Start(ctx, name, opts...)
	defer span.End()

	return body(ctx)
}
