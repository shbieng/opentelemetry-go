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

package trace

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/oteltest"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ottest "go.opentelemetry.io/otel/internal/internaltest"
	export "go.opentelemetry.io/otel/sdk/export/trace"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
)

type storingHandler struct {
	errs []error
}

func (s *storingHandler) Handle(err error) {
	s.errs = append(s.errs, err)
}

func (s *storingHandler) Reset() {
	s.errs = nil
}

var (
	tid trace.TraceID
	sid trace.SpanID

	handler *storingHandler = &storingHandler{}
)

func init() {
	tid, _ = trace.TraceIDFromHex("01020304050607080102040810203040")
	sid, _ = trace.SpanIDFromHex("0102040810203040")

	otel.SetErrorHandler(handler)
}

func TestTracerFollowsExpectedAPIBehaviour(t *testing.T) {
	tp := NewTracerProvider(WithConfig(Config{DefaultSampler: TraceIDRatioBased(0)}))
	harness := oteltest.NewHarness(t)
	subjectFactory := func() trace.Tracer {
		return tp.Tracer("")
	}

	harness.TestTracer(subjectFactory)
}

type testExporter struct {
	mu    sync.RWMutex
	idx   map[string]int
	spans []*export.SpanSnapshot
}

func NewTestExporter() *testExporter {
	return &testExporter{idx: make(map[string]int)}
}

func (te *testExporter) ExportSpans(_ context.Context, ss []*export.SpanSnapshot) error {
	te.mu.Lock()
	defer te.mu.Unlock()

	i := len(te.spans)
	for _, s := range ss {
		te.idx[s.Name] = i
		te.spans = append(te.spans, s)
		i++
	}
	return nil
}

func (te *testExporter) Spans() []*export.SpanSnapshot {
	te.mu.RLock()
	defer te.mu.RUnlock()

	cp := make([]*export.SpanSnapshot, len(te.spans))
	copy(cp, te.spans)
	return cp
}

func (te *testExporter) GetSpan(name string) (*export.SpanSnapshot, bool) {
	te.mu.RLock()
	defer te.mu.RUnlock()
	i, ok := te.idx[name]
	if !ok {
		return nil, false
	}
	return te.spans[i], true
}

func (te *testExporter) Len() int {
	te.mu.RLock()
	defer te.mu.RUnlock()
	return len(te.spans)
}

func (te *testExporter) Shutdown(context.Context) error {
	te.Reset()
	return nil
}

func (te *testExporter) Reset() {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.idx = make(map[string]int)
	te.spans = te.spans[:0]
}

type testSampler struct {
	callCount int
	prefix    string
	t         *testing.T
}

func (ts *testSampler) ShouldSample(p SamplingParameters) SamplingResult {
	ts.callCount++
	ts.t.Logf("called sampler for name %q", p.Name)
	decision := Drop
	if strings.HasPrefix(p.Name, ts.prefix) {
		decision = RecordAndSample
	}
	return SamplingResult{Decision: decision, Attributes: []attribute.KeyValue{attribute.Int("callCount", ts.callCount)}}
}

func (ts testSampler) Description() string {
	return "testSampler"
}

func TestSetName(t *testing.T) {
	tp := NewTracerProvider()

	type testCase struct {
		name    string
		newName string
	}
	for idx, tt := range []testCase{
		{ // 0
			name:    "foobar",
			newName: "foobaz",
		},
		{ // 1
			name:    "foobar",
			newName: "barbaz",
		},
		{ // 2
			name:    "barbar",
			newName: "barbaz",
		},
		{ // 3
			name:    "barbar",
			newName: "foobar",
		},
	} {
		sp := startNamedSpan(tp, "SetName", tt.name)
		if sdkspan, ok := sp.(*span); ok {
			if sdkspan.Name() != tt.name {
				t.Errorf("%d: invalid name at span creation, expected %v, got %v", idx, tt.name, sdkspan.Name())
			}
		} else {
			t.Errorf("%d: unable to coerce span to SDK span, is type %T", idx, sp)
		}
		sp.SetName(tt.newName)
		if sdkspan, ok := sp.(*span); ok {
			if sdkspan.Name() != tt.newName {
				t.Errorf("%d: span name not changed, expected %v, got %v", idx, tt.newName, sdkspan.Name())
			}
		} else {
			t.Errorf("%d: unable to coerce span to SDK span, is type %T", idx, sp)
		}
		sp.End()
	}
}

func TestRecordingIsOn(t *testing.T) {
	tp := NewTracerProvider()
	_, span := tp.Tracer("Recording on").Start(context.Background(), "StartSpan")
	defer span.End()
	if span.IsRecording() == false {
		t.Error("new span is not recording events")
	}
}

func TestSampling(t *testing.T) {
	idg := defaultIDGenerator()
	const total = 10000
	for name, tc := range map[string]struct {
		sampler       Sampler
		expect        float64
		parent        bool
		sampledParent bool
	}{
		// Span w/o a parent
		"NeverSample":           {sampler: NeverSample(), expect: 0},
		"AlwaysSample":          {sampler: AlwaysSample(), expect: 1.0},
		"TraceIdRatioBased_-1":  {sampler: TraceIDRatioBased(-1.0), expect: 0},
		"TraceIdRatioBased_.25": {sampler: TraceIDRatioBased(0.25), expect: .25},
		"TraceIdRatioBased_.50": {sampler: TraceIDRatioBased(0.50), expect: .5},
		"TraceIdRatioBased_.75": {sampler: TraceIDRatioBased(0.75), expect: .75},
		"TraceIdRatioBased_2.0": {sampler: TraceIDRatioBased(2.0), expect: 1},

		// Spans w/o a parent and using ParentBased(DelegateSampler()) Sampler, receive DelegateSampler's sampling decision
		"ParentNeverSample":           {sampler: ParentBased(NeverSample()), expect: 0},
		"ParentAlwaysSample":          {sampler: ParentBased(AlwaysSample()), expect: 1},
		"ParentTraceIdRatioBased_.50": {sampler: ParentBased(TraceIDRatioBased(0.50)), expect: .5},

		// An unadorned TraceIDRatioBased sampler ignores parent spans
		"UnsampledParentSpanWithTraceIdRatioBased_.25": {sampler: TraceIDRatioBased(0.25), expect: .25, parent: true},
		"SampledParentSpanWithTraceIdRatioBased_.25":   {sampler: TraceIDRatioBased(0.25), expect: .25, parent: true, sampledParent: true},
		"UnsampledParentSpanWithTraceIdRatioBased_.50": {sampler: TraceIDRatioBased(0.50), expect: .5, parent: true},
		"SampledParentSpanWithTraceIdRatioBased_.50":   {sampler: TraceIDRatioBased(0.50), expect: .5, parent: true, sampledParent: true},
		"UnsampledParentSpanWithTraceIdRatioBased_.75": {sampler: TraceIDRatioBased(0.75), expect: .75, parent: true},
		"SampledParentSpanWithTraceIdRatioBased_.75":   {sampler: TraceIDRatioBased(0.75), expect: .75, parent: true, sampledParent: true},

		// Spans with a sampled parent but using NeverSample Sampler, are not sampled
		"SampledParentSpanWithNeverSample": {sampler: NeverSample(), expect: 0, parent: true, sampledParent: true},

		// Spans with a sampled parent and using ParentBased(DelegateSampler()) Sampler, inherit the parent span's sampling status
		"SampledParentSpanWithParentNeverSample":             {sampler: ParentBased(NeverSample()), expect: 1, parent: true, sampledParent: true},
		"UnsampledParentSpanWithParentNeverSampler":          {sampler: ParentBased(NeverSample()), expect: 0, parent: true, sampledParent: false},
		"SampledParentSpanWithParentAlwaysSampler":           {sampler: ParentBased(AlwaysSample()), expect: 1, parent: true, sampledParent: true},
		"UnsampledParentSpanWithParentAlwaysSampler":         {sampler: ParentBased(AlwaysSample()), expect: 0, parent: true, sampledParent: false},
		"SampledParentSpanWithParentTraceIdRatioBased_.50":   {sampler: ParentBased(TraceIDRatioBased(0.50)), expect: 1, parent: true, sampledParent: true},
		"UnsampledParentSpanWithParentTraceIdRatioBased_.50": {sampler: ParentBased(TraceIDRatioBased(0.50)), expect: 0, parent: true, sampledParent: false},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := NewTracerProvider(WithConfig(Config{DefaultSampler: tc.sampler}))
			tr := p.Tracer("test")
			var sampled int
			for i := 0; i < total; i++ {
				ctx := context.Background()
				if tc.parent {
					tid, sid := idg.NewIDs(ctx)
					psc := trace.SpanContext{
						TraceID: tid,
						SpanID:  sid,
					}
					if tc.sampledParent {
						psc.TraceFlags = trace.FlagsSampled
					}
					ctx = trace.ContextWithRemoteSpanContext(ctx, psc)
				}
				_, span := tr.Start(ctx, "test")
				if span.SpanContext().IsSampled() {
					sampled++
				}
			}
			tolerance := 0.0
			got := float64(sampled) / float64(total)

			if tc.expect > 0 && tc.expect < 1 {
				// See https://en.wikipedia.org/wiki/Binomial_proportion_confidence_interval
				const z = 4.75342 // This should succeed 99.9999% of the time
				tolerance = z * math.Sqrt(got*(1-got)/total)
			}

			diff := math.Abs(got - tc.expect)
			if diff > tolerance {
				t.Errorf("got %f (diff: %f), expected %f (w/tolerance: %f)", got, diff, tc.expect, tolerance)
			}
		})
	}
}

func TestStartSpanWithParent(t *testing.T) {
	tp := NewTracerProvider()
	tr := tp.Tracer("SpanWithParent")
	ctx := context.Background()

	sc1 := trace.SpanContext{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: 0x1,
	}
	_, s1 := tr.Start(trace.ContextWithRemoteSpanContext(ctx, sc1), "span1-unsampled-parent1")
	if err := checkChild(t, sc1, s1); err != nil {
		t.Error(err)
	}

	_, s2 := tr.Start(trace.ContextWithRemoteSpanContext(ctx, sc1), "span2-unsampled-parent1")
	if err := checkChild(t, sc1, s2); err != nil {
		t.Error(err)
	}

	ts, err := trace.TraceStateFromKeyValues(attribute.String("k", "v"))
	if err != nil {
		t.Error(err)
	}
	sc2 := trace.SpanContext{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: 0x1,
		TraceState: ts,
	}
	_, s3 := tr.Start(trace.ContextWithRemoteSpanContext(ctx, sc2), "span3-sampled-parent2")
	if err := checkChild(t, sc2, s3); err != nil {
		t.Error(err)
	}

	ctx2, s4 := tr.Start(trace.ContextWithRemoteSpanContext(ctx, sc2), "span4-sampled-parent2")
	if err := checkChild(t, sc2, s4); err != nil {
		t.Error(err)
	}

	s4Sc := s4.SpanContext()
	_, s5 := tr.Start(ctx2, "span5-implicit-childof-span4")
	if err := checkChild(t, s4Sc, s5); err != nil {
		t.Error(err)
	}
}

func TestSetSpanAttributesOnStart(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
	span := startSpan(tp,
		"StartSpanAttribute",
		trace.WithAttributes(attribute.String("key1", "value1")),
		trace.WithAttributes(attribute.String("key2", "value2")),
	)
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		Attributes: []attribute.KeyValue{
			attribute.String("key1", "value1"),
			attribute.String("key2", "value2"),
		},
		SpanKind:               trace.SpanKindInternal,
		HasRemoteParent:        true,
		InstrumentationLibrary: instrumentation.Library{Name: "StartSpanAttribute"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanAttributesOnStart: -got +want %s", diff)
	}
}

func TestSetSpanAttributes(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
	span := startSpan(tp, "SpanAttribute")
	span.SetAttributes(attribute.String("key1", "value1"))
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		Attributes: []attribute.KeyValue{
			attribute.String("key1", "value1"),
		},
		SpanKind:               trace.SpanKindInternal,
		HasRemoteParent:        true,
		InstrumentationLibrary: instrumentation.Library{Name: "SpanAttribute"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanAttributes: -got +want %s", diff)
	}
}

// Test that the sampler is called for local child spans. This is verified by checking
// that the attributes set in the sampler are set on the child span.
func TestSamplerAttributesLocalChildSpan(t *testing.T) {
	sampler := &testSampler{prefix: "span", t: t}
	te := NewTestExporter()
	tp := NewTracerProvider(WithConfig(Config{DefaultSampler: sampler}), WithSyncer(te), WithResource(resource.Empty()))

	ctx := context.Background()
	ctx, span := startLocalSpan(tp, ctx, "SpanOne", "span0")
	_, spanTwo := startLocalSpan(tp, ctx, "SpanTwo", "span1")

	spanTwo.End()
	span.End()

	got := te.Spans()

	// endSpan expects only a single span in the test exporter, so manually clear the
	// fields that can't be tested for easily (times, span and trace ids).
	pid := got[0].SpanContext.SpanID
	got[0].SpanContext.TraceID = tid
	got[0].ParentSpanID = sid

	checkTime(&got[0].StartTime)
	checkTime(&got[0].EndTime)

	got[1].SpanContext.SpanID = trace.SpanID{}
	got[1].SpanContext.TraceID = tid
	got[1].ParentSpanID = pid
	got[0].SpanContext.SpanID = trace.SpanID{}

	checkTime(&got[1].StartTime)
	checkTime(&got[1].EndTime)

	want := []*export.SpanSnapshot{
		{
			SpanContext: trace.SpanContext{
				TraceID:    tid,
				TraceFlags: 0x1,
			},
			ParentSpanID:           sid,
			Name:                   "span1",
			Attributes:             []attribute.KeyValue{attribute.Int("callCount", 2)},
			SpanKind:               trace.SpanKindInternal,
			HasRemoteParent:        false,
			InstrumentationLibrary: instrumentation.Library{Name: "SpanTwo"},
		},
		{
			SpanContext: trace.SpanContext{
				TraceID:    tid,
				TraceFlags: 0x1,
			},
			ParentSpanID:           pid,
			Name:                   "span0",
			Attributes:             []attribute.KeyValue{attribute.Int("callCount", 1)},
			SpanKind:               trace.SpanKindInternal,
			HasRemoteParent:        false,
			ChildSpanCount:         1,
			InstrumentationLibrary: instrumentation.Library{Name: "SpanOne"},
		},
	}

	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanAttributesLocalChildSpan: -got +want %s", diff)
	}
}

func TestSetSpanAttributesOverLimit(t *testing.T) {
	te := NewTestExporter()
	cfg := Config{SpanLimits: SpanLimits{AttributeCountLimit: 2}}
	tp := NewTracerProvider(WithConfig(cfg), WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "SpanAttributesOverLimit")
	span.SetAttributes(
		attribute.Bool("key1", true),
		attribute.String("key2", "value2"),
		attribute.Bool("key1", false), // Replace key1.
		attribute.Int64("key4", 4),    // Remove key2 and add key4
	)
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		Attributes: []attribute.KeyValue{
			attribute.Bool("key1", false),
			attribute.Int64("key4", 4),
		},
		SpanKind:               trace.SpanKindInternal,
		HasRemoteParent:        true,
		DroppedAttributeCount:  1,
		InstrumentationLibrary: instrumentation.Library{Name: "SpanAttributesOverLimit"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanAttributesOverLimit: -got +want %s", diff)
	}
}

func TestEvents(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "Events")
	k1v1 := attribute.String("key1", "value1")
	k2v2 := attribute.Bool("key2", true)
	k3v3 := attribute.Int64("key3", 3)

	span.AddEvent("foo", trace.WithAttributes(attribute.String("key1", "value1")))
	span.AddEvent("bar", trace.WithAttributes(
		attribute.Bool("key2", true),
		attribute.Int64("key3", 3),
	))
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	for i := range got.MessageEvents {
		if !checkTime(&got.MessageEvents[i].Time) {
			t.Error("exporting span: expected nonzero Event Time")
		}
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:    sid,
		Name:            "span0",
		HasRemoteParent: true,
		MessageEvents: []trace.Event{
			{Name: "foo", Attributes: []attribute.KeyValue{k1v1}},
			{Name: "bar", Attributes: []attribute.KeyValue{k2v2, k3v3}},
		},
		SpanKind:               trace.SpanKindInternal,
		InstrumentationLibrary: instrumentation.Library{Name: "Events"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("Message Events: -got +want %s", diff)
	}
}

func TestEventsOverLimit(t *testing.T) {
	te := NewTestExporter()
	cfg := Config{SpanLimits: SpanLimits{EventCountLimit: 2}}
	tp := NewTracerProvider(WithConfig(cfg), WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "EventsOverLimit")
	k1v1 := attribute.String("key1", "value1")
	k2v2 := attribute.Bool("key2", false)
	k3v3 := attribute.String("key3", "value3")

	span.AddEvent("fooDrop", trace.WithAttributes(attribute.String("key1", "value1")))
	span.AddEvent("barDrop", trace.WithAttributes(
		attribute.Bool("key2", true),
		attribute.String("key3", "value3"),
	))
	span.AddEvent("foo", trace.WithAttributes(attribute.String("key1", "value1")))
	span.AddEvent("bar", trace.WithAttributes(
		attribute.Bool("key2", false),
		attribute.String("key3", "value3"),
	))
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	for i := range got.MessageEvents {
		if !checkTime(&got.MessageEvents[i].Time) {
			t.Error("exporting span: expected nonzero Event Time")
		}
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		MessageEvents: []trace.Event{
			{Name: "foo", Attributes: []attribute.KeyValue{k1v1}},
			{Name: "bar", Attributes: []attribute.KeyValue{k2v2, k3v3}},
		},
		DroppedMessageEventCount: 2,
		HasRemoteParent:          true,
		SpanKind:                 trace.SpanKindInternal,
		InstrumentationLibrary:   instrumentation.Library{Name: "EventsOverLimit"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("Message Event over limit: -got +want %s", diff)
	}
}

func TestLinks(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))

	k1v1 := attribute.String("key1", "value1")
	k2v2 := attribute.String("key2", "value2")
	k3v3 := attribute.String("key3", "value3")

	sc1 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}
	sc2 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}

	links := []trace.Link{
		{SpanContext: sc1, Attributes: []attribute.KeyValue{k1v1}},
		{SpanContext: sc2, Attributes: []attribute.KeyValue{k2v2, k3v3}},
	}
	span := startSpan(tp, "Links", trace.WithLinks(links...))

	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:           sid,
		Name:                   "span0",
		HasRemoteParent:        true,
		Links:                  links,
		SpanKind:               trace.SpanKindInternal,
		InstrumentationLibrary: instrumentation.Library{Name: "Links"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("Link: -got +want %s", diff)
	}
}

func TestLinksOverLimit(t *testing.T) {
	te := NewTestExporter()
	cfg := Config{SpanLimits: SpanLimits{LinkCountLimit: 2}}

	sc1 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}
	sc2 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}
	sc3 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}

	tp := NewTracerProvider(WithConfig(cfg), WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "LinksOverLimit",
		trace.WithLinks(
			trace.Link{SpanContext: sc1, Attributes: []attribute.KeyValue{attribute.String("key1", "value1")}},
			trace.Link{SpanContext: sc2, Attributes: []attribute.KeyValue{attribute.String("key2", "value2")}},
			trace.Link{SpanContext: sc3, Attributes: []attribute.KeyValue{attribute.String("key3", "value3")}},
		),
	)

	k2v2 := attribute.String("key2", "value2")
	k3v3 := attribute.String("key3", "value3")

	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		Links: []trace.Link{
			{SpanContext: sc2, Attributes: []attribute.KeyValue{k2v2}},
			{SpanContext: sc3, Attributes: []attribute.KeyValue{k3v3}},
		},
		DroppedLinkCount:       1,
		HasRemoteParent:        true,
		SpanKind:               trace.SpanKindInternal,
		InstrumentationLibrary: instrumentation.Library{Name: "LinksOverLimit"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("Link over limit: -got +want %s", diff)
	}
}

func TestSetSpanName(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
	ctx := context.Background()

	want := "SpanName-1"
	ctx = trace.ContextWithRemoteSpanContext(ctx, trace.SpanContext{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: 1,
	})
	_, span := tp.Tracer("SetSpanName").Start(ctx, "SpanName-1")
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != want {
		t.Errorf("span.Name: got %q; want %q", got.Name, want)
	}
}

func TestSetSpanStatus(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "SpanStatus")
	span.SetStatus(codes.Error, "Error")
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:           sid,
		Name:                   "span0",
		SpanKind:               trace.SpanKindInternal,
		StatusCode:             codes.Error,
		StatusMessage:          "Error",
		HasRemoteParent:        true,
		InstrumentationLibrary: instrumentation.Library{Name: "SpanStatus"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanStatus: -got +want %s", diff)
	}
}

func cmpDiff(x, y interface{}) string {
	return cmp.Diff(x, y,
		cmp.AllowUnexported(attribute.Value{}),
		cmp.AllowUnexported(trace.Event{}),
		cmp.AllowUnexported(trace.TraceState{}))
}

func remoteSpanContext() trace.SpanContext {
	return trace.SpanContext{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: 1,
	}
}

// checkChild is test utility function that tests that c has fields set appropriately,
// given that it is a child span of p.
func checkChild(t *testing.T, p trace.SpanContext, apiSpan trace.Span) error {
	s := apiSpan.(*span)
	if s == nil {
		return fmt.Errorf("got nil child span, want non-nil")
	}
	if got, want := s.spanContext.TraceID.String(), p.TraceID.String(); got != want {
		return fmt.Errorf("got child trace ID %s, want %s", got, want)
	}
	if childID, parentID := s.spanContext.SpanID.String(), p.SpanID.String(); childID == parentID {
		return fmt.Errorf("got child span ID %s, parent span ID %s; want unequal IDs", childID, parentID)
	}
	if got, want := s.spanContext.TraceFlags, p.TraceFlags; got != want {
		return fmt.Errorf("got child trace options %d, want %d", got, want)
	}
	got, want := s.spanContext.TraceState, p.TraceState
	assert.Equal(t, want, got)
	return nil
}

// startSpan starts a span with a name "span0". See startNamedSpan for
// details.
func startSpan(tp *TracerProvider, trName string, args ...trace.SpanOption) trace.Span {
	return startNamedSpan(tp, trName, "span0", args...)
}

// startNamed Span is a test utility func that starts a span with a
// passed name and with remote span context as parent. The remote span
// context contains TraceFlags with sampled bit set. This allows the
// span to be automatically sampled.
func startNamedSpan(tp *TracerProvider, trName, name string, args ...trace.SpanOption) trace.Span {
	ctx := context.Background()
	ctx = trace.ContextWithRemoteSpanContext(ctx, remoteSpanContext())
	args = append(args, trace.WithRecord())
	_, span := tp.Tracer(trName).Start(
		ctx,
		name,
		args...,
	)
	return span
}

// startLocalSpan is a test utility func that starts a span with a
// passed name and with the passed context. The context is returned
// along with the span so this parent can be used to create child
// spans.
func startLocalSpan(tp *TracerProvider, ctx context.Context, trName, name string, args ...trace.SpanOption) (context.Context, trace.Span) {
	args = append(args, trace.WithRecord())
	ctx, span := tp.Tracer(trName).Start(
		ctx,
		name,
		args...,
	)
	return ctx, span
}

// endSpan is a test utility function that ends the span in the context and
// returns the exported export.SpanSnapshot.
// It requires that span be sampled using one of these methods
//  1. Passing parent span context in context
//  2. Use WithSampler(AlwaysSample())
//  3. Configuring AlwaysSample() as default sampler
//
// It also does some basic tests on the span.
// It also clears spanID in the export.SpanSnapshot to make the comparison
// easier.
func endSpan(te *testExporter, span trace.Span) (*export.SpanSnapshot, error) {
	if !span.IsRecording() {
		return nil, fmt.Errorf("IsRecording: got false, want true")
	}
	if !span.SpanContext().IsSampled() {
		return nil, fmt.Errorf("IsSampled: got false, want true")
	}
	span.End()
	if te.Len() != 1 {
		return nil, fmt.Errorf("got %d exported spans, want one span", te.Len())
	}
	got := te.Spans()[0]
	if !got.SpanContext.SpanID.IsValid() {
		return nil, fmt.Errorf("exporting span: expected nonzero SpanID")
	}
	got.SpanContext.SpanID = trace.SpanID{}
	if !checkTime(&got.StartTime) {
		return nil, fmt.Errorf("exporting span: expected nonzero StartTime")
	}
	if !checkTime(&got.EndTime) {
		return nil, fmt.Errorf("exporting span: expected nonzero EndTime")
	}
	return got, nil
}

// checkTime checks that a nonzero time was set in x, then clears it.
func checkTime(x *time.Time) bool {
	if x.IsZero() {
		return false
	}
	*x = time.Time{}
	return true
}

func TestEndSpanTwice(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te))

	st := time.Now()
	et1 := st.Add(100 * time.Millisecond)
	et2 := st.Add(200 * time.Millisecond)

	span := startSpan(tp, "EndSpanTwice", trace.WithTimestamp(st))
	span.End(trace.WithTimestamp(et1))
	span.End(trace.WithTimestamp(et2))

	if te.Len() != 1 {
		t.Fatalf("expected only a single span, got %#v", te.Spans())
	}

	ro := span.(ReadOnlySpan)
	if ro.EndTime() != et1 {
		t.Fatalf("2nd call to End() should not modify end time")
	}
}

func TestStartSpanAfterEnd(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithConfig(Config{DefaultSampler: AlwaysSample()}), WithSyncer(te))
	ctx := context.Background()

	tr := tp.Tracer("SpanAfterEnd")
	ctx, span0 := tr.Start(trace.ContextWithRemoteSpanContext(ctx, remoteSpanContext()), "parent")
	ctx1, span1 := tr.Start(ctx, "span-1")
	span1.End()
	// Start a new span with the context containing span-1
	// even though span-1 is ended, we still add this as a new child of span-1
	_, span2 := tr.Start(ctx1, "span-2")
	span2.End()
	span0.End()
	if got, want := te.Len(), 3; got != want {
		t.Fatalf("len(%#v) = %d; want %d", te.Spans(), got, want)
	}

	gotParent, ok := te.GetSpan("parent")
	if !ok {
		t.Fatal("parent not recorded")
	}
	gotSpan1, ok := te.GetSpan("span-1")
	if !ok {
		t.Fatal("span-1 not recorded")
	}
	gotSpan2, ok := te.GetSpan("span-2")
	if !ok {
		t.Fatal("span-2 not recorded")
	}

	if got, want := gotSpan1.SpanContext.TraceID, gotParent.SpanContext.TraceID; got != want {
		t.Errorf("span-1.TraceID=%q; want %q", got, want)
	}
	if got, want := gotSpan2.SpanContext.TraceID, gotParent.SpanContext.TraceID; got != want {
		t.Errorf("span-2.TraceID=%q; want %q", got, want)
	}
	if got, want := gotSpan1.ParentSpanID, gotParent.SpanContext.SpanID; got != want {
		t.Errorf("span-1.ParentSpanID=%q; want %q (parent.SpanID)", got, want)
	}
	if got, want := gotSpan2.ParentSpanID, gotSpan1.SpanContext.SpanID; got != want {
		t.Errorf("span-2.ParentSpanID=%q; want %q (span1.SpanID)", got, want)
	}
}

func TestChildSpanCount(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithConfig(Config{DefaultSampler: AlwaysSample()}), WithSyncer(te))

	tr := tp.Tracer("ChidSpanCount")
	ctx, span0 := tr.Start(context.Background(), "parent")
	ctx1, span1 := tr.Start(ctx, "span-1")
	_, span2 := tr.Start(ctx1, "span-2")
	span2.End()
	span1.End()

	_, span3 := tr.Start(ctx, "span-3")
	span3.End()
	span0.End()
	if got, want := te.Len(), 4; got != want {
		t.Fatalf("len(%#v) = %d; want %d", te.Spans(), got, want)
	}

	gotParent, ok := te.GetSpan("parent")
	if !ok {
		t.Fatal("parent not recorded")
	}
	gotSpan1, ok := te.GetSpan("span-1")
	if !ok {
		t.Fatal("span-1 not recorded")
	}
	gotSpan2, ok := te.GetSpan("span-2")
	if !ok {
		t.Fatal("span-2 not recorded")
	}
	gotSpan3, ok := te.GetSpan("span-3")
	if !ok {
		t.Fatal("span-3 not recorded")
	}

	if got, want := gotSpan3.ChildSpanCount, 0; got != want {
		t.Errorf("span-3.ChildSpanCount=%d; want %d", got, want)
	}
	if got, want := gotSpan2.ChildSpanCount, 0; got != want {
		t.Errorf("span-2.ChildSpanCount=%d; want %d", got, want)
	}
	if got, want := gotSpan1.ChildSpanCount, 1; got != want {
		t.Errorf("span-1.ChildSpanCount=%d; want %d", got, want)
	}
	if got, want := gotParent.ChildSpanCount, 2; got != want {
		t.Errorf("parent.ChildSpanCount=%d; want %d", got, want)
	}
}

func TestNilSpanEnd(t *testing.T) {
	var span *span
	span.End()
}

func TestExecutionTracerTaskEnd(t *testing.T) {
	var n uint64
	tp := NewTracerProvider(WithConfig(Config{DefaultSampler: NeverSample()}))
	tr := tp.Tracer("Execution Tracer Task End")

	executionTracerTaskEnd := func() {
		atomic.AddUint64(&n, 1)
	}

	var spans []*span
	_, apiSpan := tr.Start(context.Background(), "foo")
	s := apiSpan.(*span)

	s.executionTracerTaskEnd = executionTracerTaskEnd
	spans = append(spans, s) // never sample

	tID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f")
	sID, _ := trace.SpanIDFromHex("0001020304050607")
	ctx := context.Background()

	ctx = trace.ContextWithRemoteSpanContext(ctx,
		trace.SpanContext{
			TraceID:    tID,
			SpanID:     sID,
			TraceFlags: 0,
		},
	)
	_, apiSpan = tr.Start(
		ctx,
		"foo",
	)
	s = apiSpan.(*span)
	s.executionTracerTaskEnd = executionTracerTaskEnd
	spans = append(spans, s) // parent not sampled

	// tp.ApplyConfig(Config{DefaultSampler: AlwaysSample()})
	_, apiSpan = tr.Start(context.Background(), "foo")
	s = apiSpan.(*span)
	s.executionTracerTaskEnd = executionTracerTaskEnd
	spans = append(spans, s) // always sample

	for _, span := range spans {
		span.End()
	}
	if got, want := n, uint64(len(spans)); got != want {
		t.Fatalf("Execution tracer task ended for %v spans; want %v", got, want)
	}
}

func TestCustomStartEndTime(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithConfig(Config{DefaultSampler: AlwaysSample()}))

	startTime := time.Date(2019, time.August, 27, 14, 42, 0, 0, time.UTC)
	endTime := startTime.Add(time.Second * 20)
	_, span := tp.Tracer("Custom Start and End time").Start(
		context.Background(),
		"testspan",
		trace.WithTimestamp(startTime),
	)
	span.End(trace.WithTimestamp(endTime))

	if te.Len() != 1 {
		t.Fatalf("got %d exported spans, want one span", te.Len())
	}
	got := te.Spans()[0]
	if got.StartTime != startTime {
		t.Errorf("expected start time to be %s, got %s", startTime, got.StartTime)
	}
	if got.EndTime != endTime {
		t.Errorf("expected end time to be %s, got %s", endTime, got.EndTime)
	}
}

func TestRecordError(t *testing.T) {
	scenarios := []struct {
		err error
		typ string
		msg string
	}{
		{
			err: ottest.NewTestError("test error"),
			typ: "go.opentelemetry.io/otel/internal/internaltest.TestError",
			msg: "test error",
		},
		{
			err: errors.New("test error 2"),
			typ: "*errors.errorString",
			msg: "test error 2",
		},
	}

	for _, s := range scenarios {
		te := NewTestExporter()
		tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
		span := startSpan(tp, "RecordError")

		errTime := time.Now()
		span.RecordError(s.err, trace.WithTimestamp(errTime))

		got, err := endSpan(te, span)
		if err != nil {
			t.Fatal(err)
		}

		want := &export.SpanSnapshot{
			SpanContext: trace.SpanContext{
				TraceID:    tid,
				TraceFlags: 0x1,
			},
			ParentSpanID:    sid,
			Name:            "span0",
			StatusCode:      codes.Error,
			SpanKind:        trace.SpanKindInternal,
			HasRemoteParent: true,
			MessageEvents: []trace.Event{
				{
					Name: errorEventName,
					Time: errTime,
					Attributes: []attribute.KeyValue{
						errorTypeKey.String(s.typ),
						errorMessageKey.String(s.msg),
					},
				},
			},
			InstrumentationLibrary: instrumentation.Library{Name: "RecordError"},
		}
		if diff := cmpDiff(got, want); diff != "" {
			t.Errorf("SpanErrorOptions: -got +want %s", diff)
		}
	}
}

func TestRecordErrorNil(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
	span := startSpan(tp, "RecordErrorNil")

	span.RecordError(nil)

	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:           sid,
		Name:                   "span0",
		SpanKind:               trace.SpanKindInternal,
		HasRemoteParent:        true,
		StatusCode:             codes.Unset,
		StatusMessage:          "",
		InstrumentationLibrary: instrumentation.Library{Name: "RecordErrorNil"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SpanErrorOptions: -got +want %s", diff)
	}
}

func TestWithSpanKind(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithConfig(Config{DefaultSampler: AlwaysSample()}), WithResource(resource.Empty()))
	tr := tp.Tracer("withSpanKind")

	_, span := tr.Start(context.Background(), "WithoutSpanKind")
	spanData, err := endSpan(te, span)
	if err != nil {
		t.Error(err.Error())
	}

	if spanData.SpanKind != trace.SpanKindInternal {
		t.Errorf("Default value of Spankind should be Internal: got %+v, want %+v\n", spanData.SpanKind, trace.SpanKindInternal)
	}

	sks := []trace.SpanKind{
		trace.SpanKindInternal,
		trace.SpanKindServer,
		trace.SpanKindClient,
		trace.SpanKindProducer,
		trace.SpanKindConsumer,
	}

	for _, sk := range sks {
		te.Reset()

		_, span := tr.Start(context.Background(), fmt.Sprintf("SpanKind-%v", sk), trace.WithSpanKind(sk))
		spanData, err := endSpan(te, span)
		if err != nil {
			t.Error(err.Error())
		}

		if spanData.SpanKind != sk {
			t.Errorf("WithSpanKind check: got %+v, want %+v\n", spanData.SpanKind, sks)
		}
	}
}

func TestWithResource(t *testing.T) {
	cases := []struct {
		name    string
		options []TracerProviderOption
		want    *resource.Resource
		msg     string
	}{
		{
			name:    "explicitly empty resource",
			options: []TracerProviderOption{WithResource(resource.Empty())},
			want:    resource.Empty(),
		},
		{
			name:    "uses default if no resource option",
			options: []TracerProviderOption{},
			want:    resource.Default(),
		},
		{
			name:    "explicit resource",
			options: []TracerProviderOption{WithResource(resource.NewWithAttributes(attribute.String("rk1", "rv1"), attribute.Int64("rk2", 5)))},
			want:    resource.NewWithAttributes(attribute.String("rk1", "rv1"), attribute.Int64("rk2", 5)),
		},
		{
			name: "last resource wins",
			options: []TracerProviderOption{
				WithResource(resource.NewWithAttributes(attribute.String("rk1", "vk1"), attribute.Int64("rk2", 5))),
				WithResource(resource.NewWithAttributes(attribute.String("rk3", "rv3"), attribute.Int64("rk4", 10)))},
			want: resource.NewWithAttributes(attribute.String("rk3", "rv3"), attribute.Int64("rk4", 10)),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			te := NewTestExporter()
			defaultOptions := []TracerProviderOption{WithSyncer(te), WithConfig(Config{DefaultSampler: AlwaysSample()})}
			tp := NewTracerProvider(append(defaultOptions, tc.options...)...)
			span := startSpan(tp, "WithResource")
			span.SetAttributes(attribute.String("key1", "value1"))
			got, err := endSpan(te, span)
			if err != nil {
				t.Error(err.Error())
			}
			want := &export.SpanSnapshot{
				SpanContext: trace.SpanContext{
					TraceID:    tid,
					TraceFlags: 0x1,
				},
				ParentSpanID: sid,
				Name:         "span0",
				Attributes: []attribute.KeyValue{
					attribute.String("key1", "value1"),
				},
				SpanKind:               trace.SpanKindInternal,
				HasRemoteParent:        true,
				Resource:               tc.want,
				InstrumentationLibrary: instrumentation.Library{Name: "WithResource"},
			}
			if diff := cmpDiff(got, want); diff != "" {
				t.Errorf("WithResource:\n  -got +want %s", diff)
			}
		})
	}
}

func TestWithInstrumentationVersion(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))

	ctx := context.Background()
	ctx = trace.ContextWithRemoteSpanContext(ctx, remoteSpanContext())
	_, span := tp.Tracer(
		"WithInstrumentationVersion",
		trace.WithInstrumentationVersion("v0.1.0"),
	).Start(ctx, "span0", trace.WithRecord())
	got, err := endSpan(te, span)
	if err != nil {
		t.Error(err.Error())
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:    sid,
		Name:            "span0",
		SpanKind:        trace.SpanKindInternal,
		HasRemoteParent: true,
		InstrumentationLibrary: instrumentation.Library{
			Name:    "WithInstrumentationVersion",
			Version: "v0.1.0",
		},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("WithResource:\n  -got +want %s", diff)
	}
}

func TestSpanCapturesPanic(t *testing.T) {
	te := NewTestExporter()
	tp := NewTracerProvider(WithSyncer(te), WithResource(resource.Empty()))
	_, span := tp.Tracer("CatchPanic").Start(
		context.Background(),
		"span",
		trace.WithRecord(),
	)

	f := func() {
		defer span.End()
		panic(errors.New("error message"))
	}
	require.PanicsWithError(t, "error message", f)
	spans := te.Spans()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].MessageEvents, 1)
	assert.Equal(t, spans[0].MessageEvents[0].Name, errorEventName)
	assert.Equal(t, spans[0].MessageEvents[0].Attributes, []attribute.KeyValue{
		errorTypeKey.String("*errors.errorString"),
		errorMessageKey.String("error message"),
	})
}

func TestReadOnlySpan(t *testing.T) {
	kv := attribute.String("foo", "bar")

	tp := NewTracerProvider(WithResource(resource.NewWithAttributes(kv)))
	cfg := tp.config.Load().(*Config)
	tr := tp.Tracer("ReadOnlySpan", trace.WithInstrumentationVersion("3"))

	// Initialize parent context.
	tID, sID := cfg.IDGenerator.NewIDs(context.Background())
	parent := trace.SpanContext{
		TraceID:    tID,
		SpanID:     sID,
		TraceFlags: 0x1,
	}
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)

	// Initialize linked context.
	tID, sID = cfg.IDGenerator.NewIDs(context.Background())
	linked := trace.SpanContext{
		TraceID:    tID,
		SpanID:     sID,
		TraceFlags: 0x1,
	}

	st := time.Now()
	ctx, span := tr.Start(ctx, "foo", trace.WithTimestamp(st),
		trace.WithLinks(trace.Link{SpanContext: linked}))
	span.SetAttributes(kv)
	span.AddEvent("foo", trace.WithAttributes(kv))
	span.SetStatus(codes.Ok, "foo")

	// Verify span implements ReadOnlySpan.
	ro, ok := span.(ReadOnlySpan)
	require.True(t, ok)

	assert.Equal(t, "foo", ro.Name())
	assert.Equal(t, trace.SpanContextFromContext(ctx), ro.SpanContext())
	assert.Equal(t, parent, ro.Parent())
	assert.Equal(t, trace.SpanKindInternal, ro.SpanKind())
	assert.Equal(t, st, ro.StartTime())
	assert.True(t, ro.EndTime().IsZero())
	assert.Equal(t, kv.Key, ro.Attributes()[0].Key)
	assert.Equal(t, kv.Value, ro.Attributes()[0].Value)
	assert.Equal(t, linked, ro.Links()[0].SpanContext)
	assert.Equal(t, kv.Key, ro.Events()[0].Attributes[0].Key)
	assert.Equal(t, kv.Value, ro.Events()[0].Attributes[0].Value)
	assert.Equal(t, codes.Ok, ro.StatusCode())
	assert.Equal(t, "foo", ro.StatusMessage())
	assert.Equal(t, "ReadOnlySpan", ro.InstrumentationLibrary().Name)
	assert.Equal(t, "3", ro.InstrumentationLibrary().Version)
	assert.Equal(t, kv.Key, ro.Resource().Attributes()[0].Key)
	assert.Equal(t, kv.Value, ro.Resource().Attributes()[0].Value)

	// Verify changes to the original span are reflected in the ReadOnlySpan.
	span.SetName("bar")
	assert.Equal(t, "bar", ro.Name())

	// Verify Snapshot() returns snapshots that are independent from the
	// original span and from one another.
	d1 := ro.Snapshot()
	span.AddEvent("baz")
	d2 := ro.Snapshot()
	for _, e := range d1.MessageEvents {
		if e.Name == "baz" {
			t.Errorf("Didn't expect to find 'baz' event")
		}
	}
	var exists bool
	for _, e := range d2.MessageEvents {
		if e.Name == "baz" {
			exists = true
		}
	}
	if !exists {
		t.Errorf("Expected to find 'baz' event")
	}

	et := st.Add(time.Millisecond)
	span.End(trace.WithTimestamp(et))
	assert.Equal(t, et, ro.EndTime())
}

func TestReadWriteSpan(t *testing.T) {
	tp := NewTracerProvider(WithResource(resource.Empty()))
	cfg := tp.config.Load().(*Config)
	tr := tp.Tracer("ReadWriteSpan")

	// Initialize parent context.
	tID, sID := cfg.IDGenerator.NewIDs(context.Background())
	parent := trace.SpanContext{
		TraceID:    tID,
		SpanID:     sID,
		TraceFlags: 0x1,
	}
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)

	_, span := tr.Start(ctx, "foo")
	defer span.End()

	// Verify span implements ReadOnlySpan.
	rw, ok := span.(ReadWriteSpan)
	require.True(t, ok)

	// Verify the span can be read from.
	assert.False(t, rw.StartTime().IsZero())

	// Verify the span can be written to.
	rw.SetName("bar")
	assert.Equal(t, "bar", rw.Name())

	// NOTE: This function tests ReadWriteSpan which is an interface which
	// embeds trace.Span and ReadOnlySpan. Since both of these interfaces have
	// their own tests, there is no point in testing all the possible methods
	// available via ReadWriteSpan as doing so would mean creating a lot of
	// duplication.
}

func TestAddEventsWithMoreAttributesThanLimit(t *testing.T) {
	te := NewTestExporter()
	cfg := Config{SpanLimits: SpanLimits{AttributePerEventCountLimit: 2}}
	tp := NewTracerProvider(WithConfig(cfg), WithSyncer(te), WithResource(resource.Empty()))

	span := startSpan(tp, "AddSpanEventWithOverLimitedAttributes")
	span.AddEvent("test1", trace.WithAttributes(
		attribute.Bool("key1", true),
		attribute.String("key2", "value2"),
	))
	// Parts of the attribute should be discard
	span.AddEvent("test2", trace.WithAttributes(
		attribute.Bool("key1", true),
		attribute.String("key2", "value2"),
		attribute.String("key3", "value3"),
		attribute.String("key4", "value4"),
	))
	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	for i := range got.MessageEvents {
		if !checkTime(&got.MessageEvents[i].Time) {
			t.Error("exporting span: expected nonzero Event Time")
		}
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID: sid,
		Name:         "span0",
		Attributes:   nil,
		MessageEvents: []trace.Event{
			{
				Name: "test1",
				Attributes: []attribute.KeyValue{
					attribute.Bool("key1", true),
					attribute.String("key2", "value2"),
				},
			},
			{
				Name: "test2",
				Attributes: []attribute.KeyValue{
					attribute.Bool("key1", true),
					attribute.String("key2", "value2"),
				},
			},
		},
		SpanKind:               trace.SpanKindInternal,
		HasRemoteParent:        true,
		DroppedAttributeCount:  2,
		InstrumentationLibrary: instrumentation.Library{Name: "AddSpanEventWithOverLimitedAttributes"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("SetSpanAttributesOverLimit: -got +want %s", diff)
	}
}

func TestAddLinksWithMoreAttributesThanLimit(t *testing.T) {
	te := NewTestExporter()
	cfg := Config{SpanLimits: SpanLimits{AttributePerLinkCountLimit: 1}}
	tp := NewTracerProvider(WithConfig(cfg), WithSyncer(te), WithResource(resource.Empty()))

	k1v1 := attribute.String("key1", "value1")
	k2v2 := attribute.String("key2", "value2")
	k3v3 := attribute.String("key3", "value3")
	k4v4 := attribute.String("key4", "value4")

	sc1 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}
	sc2 := trace.SpanContext{TraceID: trace.TraceID([16]byte{1, 1}), SpanID: trace.SpanID{3}}

	span := startSpan(tp, "Links", trace.WithLinks([]trace.Link{
		{SpanContext: sc1, Attributes: []attribute.KeyValue{k1v1, k2v2}},
		{SpanContext: sc2, Attributes: []attribute.KeyValue{k2v2, k3v3, k4v4}},
	}...))

	got, err := endSpan(te, span)
	if err != nil {
		t.Fatal(err)
	}

	want := &export.SpanSnapshot{
		SpanContext: trace.SpanContext{
			TraceID:    tid,
			TraceFlags: 0x1,
		},
		ParentSpanID:    sid,
		Name:            "span0",
		HasRemoteParent: true,
		Links: []trace.Link{
			{SpanContext: sc1, Attributes: []attribute.KeyValue{k1v1}},
			{SpanContext: sc2, Attributes: []attribute.KeyValue{k2v2}},
		},
		DroppedAttributeCount:  3,
		SpanKind:               trace.SpanKindInternal,
		InstrumentationLibrary: instrumentation.Library{Name: "Links"},
	}
	if diff := cmpDiff(got, want); diff != "" {
		t.Errorf("Link: -got +want %s", diff)
	}
}
