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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
)

func TestIsValid(t *testing.T) {
	for _, testcase := range []struct {
		name string
		tid  TraceID
		sid  SpanID
		want bool
	}{
		{
			name: "SpanContext.IsValid() returns true if sc has both an Trace ID and Span ID",
			tid:  [16]byte{1},
			sid:  [8]byte{42},
			want: true,
		}, {
			name: "SpanContext.IsValid() returns false if sc has neither an Trace ID nor Span ID",
			tid:  TraceID([16]byte{}),
			sid:  [8]byte{},
			want: false,
		}, {
			name: "SpanContext.IsValid() returns false if sc has a Span ID but not a Trace ID",
			tid:  TraceID([16]byte{}),
			sid:  [8]byte{42},
			want: false,
		}, {
			name: "SpanContext.IsValid() returns false if sc has a Trace ID but not a Span ID",
			tid:  TraceID([16]byte{1}),
			sid:  [8]byte{},
			want: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			sc := SpanContext{
				traceID: testcase.tid,
				spanID:  testcase.sid,
			}
			have := sc.IsValid()
			if have != testcase.want {
				t.Errorf("Want: %v, but have: %v", testcase.want, have)
			}
		})
	}
}

func TestIsValidFromHex(t *testing.T) {
	for _, testcase := range []struct {
		name  string
		hex   string
		tid   TraceID
		valid bool
	}{
		{
			name:  "Valid TraceID",
			tid:   TraceID([16]byte{128, 241, 152, 238, 86, 52, 59, 168, 100, 254, 139, 42, 87, 211, 239, 247}),
			hex:   "80f198ee56343ba864fe8b2a57d3eff7",
			valid: true,
		}, {
			name:  "Invalid TraceID with invalid length",
			hex:   "80f198ee56343ba864fe8b2a57d3eff",
			valid: false,
		}, {
			name:  "Invalid TraceID with invalid char",
			hex:   "80f198ee56343ba864fe8b2a57d3efg7",
			valid: false,
		}, {
			name:  "Invalid TraceID with uppercase",
			hex:   "80f198ee56343ba864fe8b2a57d3efF7",
			valid: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			tid, err := TraceIDFromHex(testcase.hex)

			if testcase.valid && err != nil {
				t.Errorf("Expected TraceID %s to be valid but end with error %s", testcase.hex, err.Error())
			}

			if !testcase.valid && err == nil {
				t.Errorf("Expected TraceID %s to be invalid but end no error", testcase.hex)
			}

			if tid != testcase.tid {
				t.Errorf("Want: %v, but have: %v", testcase.tid, tid)
			}
		})
	}
}

func TestHasTraceID(t *testing.T) {
	for _, testcase := range []struct {
		name string
		tid  TraceID
		want bool
	}{
		{
			name: "SpanContext.HasTraceID() returns true if both Low and High are nonzero",
			tid:  TraceID([16]byte{1}),
			want: true,
		}, {
			name: "SpanContext.HasTraceID() returns false if neither Low nor High are nonzero",
			tid:  TraceID{},
			want: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			//proto: func (sc SpanContext) HasTraceID() bool{}
			sc := SpanContext{traceID: testcase.tid}
			have := sc.HasTraceID()
			if have != testcase.want {
				t.Errorf("Want: %v, but have: %v", testcase.want, have)
			}
		})
	}
}

func TestHasSpanID(t *testing.T) {
	for _, testcase := range []struct {
		name string
		sc   SpanContext
		want bool
	}{
		{
			name: "SpanContext.HasSpanID() returns true if self.SpanID != 0",
			sc:   SpanContext{spanID: [8]byte{42}},
			want: true,
		}, {
			name: "SpanContext.HasSpanID() returns false if self.SpanID == 0",
			sc:   SpanContext{},
			want: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			//proto: func (sc SpanContext) HasSpanID() bool {}
			have := testcase.sc.HasSpanID()
			if have != testcase.want {
				t.Errorf("Want: %v, but have: %v", testcase.want, have)
			}
		})
	}
}

func TestTraceFlagsIsSampled(t *testing.T) {
	for _, testcase := range []struct {
		name string
		tf   TraceFlags
		want bool
	}{
		{
			name: "sampled",
			tf:   FlagsSampled,
			want: true,
		}, {
			name: "unused bits are ignored, still not sampled",
			tf:   ^FlagsSampled,
			want: false,
		}, {
			name: "unused bits are ignored, still sampled",
			tf:   FlagsSampled | ^FlagsSampled,
			want: true,
		}, {
			name: "not sampled/default",
			want: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			have := testcase.tf.IsSampled()
			if have != testcase.want {
				t.Errorf("Want: %v, but have: %v", testcase.want, have)
			}
		})
	}
}

func TestTraceFlagsWithSampled(t *testing.T) {
	for _, testcase := range []struct {
		name   string
		start  TraceFlags
		sample bool
		want   TraceFlags
	}{
		{
			name:   "sampled unchanged",
			start:  FlagsSampled,
			want:   FlagsSampled,
			sample: true,
		}, {
			name:   "become sampled",
			want:   FlagsSampled,
			sample: true,
		}, {
			name:   "unused bits are ignored, still not sampled",
			start:  ^FlagsSampled,
			want:   ^FlagsSampled,
			sample: false,
		}, {
			name:   "unused bits are ignored, becomes sampled",
			start:  ^FlagsSampled,
			want:   FlagsSampled | ^FlagsSampled,
			sample: true,
		}, {
			name:   "not sampled/default",
			sample: false,
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			have := testcase.start.WithSampled(testcase.sample)
			if have != testcase.want {
				t.Errorf("Want: %v, but have: %v", testcase.want, have)
			}
		})
	}
}

func TestStringTraceID(t *testing.T) {
	for _, testcase := range []struct {
		name string
		tid  TraceID
		want string
	}{
		{
			name: "TraceID.String returns string representation of self.TraceID values > 0",
			tid:  TraceID([16]byte{255}),
			want: "ff000000000000000000000000000000",
		},
		{
			name: "TraceID.String returns string representation of self.TraceID values == 0",
			tid:  TraceID([16]byte{}),
			want: "00000000000000000000000000000000",
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			//proto: func (t TraceID) String() string {}
			have := testcase.tid.String()
			if have != testcase.want {
				t.Errorf("Want: %s, but have: %s", testcase.want, have)
			}
		})
	}
}

func TestStringSpanID(t *testing.T) {
	for _, testcase := range []struct {
		name string
		sid  SpanID
		want string
	}{
		{
			name: "SpanID.String returns string representation of self.SpanID values > 0",
			sid:  SpanID([8]byte{255}),
			want: "ff00000000000000",
		},
		{
			name: "SpanID.String returns string representation of self.SpanID values == 0",
			sid:  SpanID([8]byte{}),
			want: "0000000000000000",
		},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			//proto: func (t TraceID) String() string {}
			have := testcase.sid.String()
			if have != testcase.want {
				t.Errorf("Want: %s, but have: %s", testcase.want, have)
			}
		})
	}
}

func TestValidateSpanKind(t *testing.T) {
	tests := []struct {
		in   SpanKind
		want SpanKind
	}{
		{
			SpanKindUnspecified,
			SpanKindInternal,
		},
		{

			SpanKindInternal,
			SpanKindInternal,
		},
		{

			SpanKindServer,
			SpanKindServer,
		},
		{

			SpanKindClient,
			SpanKindClient,
		},
		{
			SpanKindProducer,
			SpanKindProducer,
		},
		{
			SpanKindConsumer,
			SpanKindConsumer,
		},
	}
	for _, test := range tests {
		if got := ValidateSpanKind(test.in); got != test.want {
			t.Errorf("ValidateSpanKind(%#v) = %#v, want %#v", test.in, got, test.want)
		}
	}
}

func TestSpanKindString(t *testing.T) {
	tests := []struct {
		in   SpanKind
		want string
	}{
		{
			SpanKindUnspecified,
			"unspecified",
		},
		{

			SpanKindInternal,
			"internal",
		},
		{

			SpanKindServer,
			"server",
		},
		{

			SpanKindClient,
			"client",
		},
		{
			SpanKindProducer,
			"producer",
		},
		{
			SpanKindConsumer,
			"consumer",
		},
	}
	for _, test := range tests {
		if got := test.in.String(); got != test.want {
			t.Errorf("%#v.String() = %#v, want %#v", test.in, got, test.want)
		}
	}
}

func TestTraceStateString(t *testing.T) {
	testCases := []struct {
		name        string
		traceState  TraceState
		expectedStr string
	}{
		{
			name: "Non-empty trace state",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3@vendor", "val3"),
				},
			},
			expectedStr: "key1=val1,key2=val2,key3@vendor=val3",
		},
		{
			name:        "Empty trace state",
			traceState:  TraceState{},
			expectedStr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedStr, tc.traceState.String())
		})
	}
}

func TestTraceStateGet(t *testing.T) {
	testCases := []struct {
		name          string
		traceState    TraceState
		key           attribute.Key
		expectedValue string
	}{
		{
			name:          "OK case",
			traceState:    TraceState{kvsWithMaxMembers},
			key:           "key16",
			expectedValue: "value16",
		},
		{
			name:          "Not found",
			traceState:    TraceState{kvsWithMaxMembers},
			key:           "keyxx",
			expectedValue: "",
		},
		{
			name:          "Invalid key",
			traceState:    TraceState{kvsWithMaxMembers},
			key:           "key!",
			expectedValue: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kv := tc.traceState.Get(tc.key)
			assert.Equal(t, tc.expectedValue, kv.AsString())
		})
	}
}

func TestTraceStateDelete(t *testing.T) {
	testCases := []struct {
		name               string
		traceState         TraceState
		key                attribute.Key
		expectedTraceState TraceState
		expectedErr        error
	}{
		{
			name: "OK case",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			key: "key2",
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key3", "val3"),
				},
			},
		},
		{
			name: "Non-existing key",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			key: "keyx",
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
		},
		{
			name: "Invalid key",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			key: "in va lid",
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			expectedErr: errInvalidTraceStateKeyValue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.traceState.Delete(tc.key)
			if tc.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tc.expectedErr, err)
				assert.Equal(t, tc.traceState, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedTraceState, result)
			}
		})
	}
}

func TestTraceStateInsert(t *testing.T) {
	testCases := []struct {
		name               string
		traceState         TraceState
		keyValue           attribute.KeyValue
		expectedTraceState TraceState
		expectedErr        error
	}{
		{
			name: "OK case - add new",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			keyValue: attribute.String("key4@vendor", "val4"),
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key4@vendor", "val4"),
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
		},
		{
			name: "OK case - replace",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			keyValue: attribute.String("key2", "valX"),
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key2", "valX"),
					attribute.String("key1", "val1"),
					attribute.String("key3", "val3"),
				},
			},
		},
		{
			name: "Invalid key/value",
			traceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
				},
			},
			keyValue: attribute.String("key!", "val!"),
			expectedTraceState: TraceState{
				kvs: []attribute.KeyValue{
					attribute.String("key1", "val1"),
				},
			},
			expectedErr: errInvalidTraceStateKeyValue,
		},
		{
			name:               "Too many entries",
			traceState:         TraceState{kvsWithMaxMembers},
			keyValue:           attribute.String("keyx", "valx"),
			expectedTraceState: TraceState{kvsWithMaxMembers},
			expectedErr:        errInvalidTraceStateMembersNumber,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.traceState.Insert(tc.keyValue)
			if tc.expectedErr != nil {
				require.Error(t, err)
				assert.Equal(t, tc.expectedErr, err)
				assert.Equal(t, tc.traceState, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedTraceState, result)
			}
		})
	}
}

func TestTraceStateFromKeyValues(t *testing.T) {
	testCases := []struct {
		name               string
		kvs                []attribute.KeyValue
		expectedTraceState TraceState
		expectedErr        error
	}{
		{
			name:               "OK case",
			kvs:                kvsWithMaxMembers,
			expectedTraceState: TraceState{kvsWithMaxMembers},
		},
		{
			name:               "OK case (empty)",
			expectedTraceState: TraceState{},
		},
		{
			name: "Too many entries",
			kvs: func() []attribute.KeyValue {
				kvs := kvsWithMaxMembers
				kvs = append(kvs, attribute.String("keyx", "valX"))
				return kvs
			}(),
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateMembersNumber,
		},
		{
			name: "Duplicate key",
			kvs: []attribute.KeyValue{
				attribute.String("key1", "val1"),
				attribute.String("key1", "val2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateDuplicate,
		},
		{
			name: "Duplicate key/value",
			kvs: []attribute.KeyValue{
				attribute.String("key1", "val1"),
				attribute.String("key1", "val1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateDuplicate,
		},
		{
			name: "Invalid key/value",
			kvs: []attribute.KeyValue{
				attribute.String("key!", "val!"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Full character set",
			kvs: []attribute.KeyValue{
				attribute.String(
					"abcdefghijklmnopqrstuvwxyz0123456789_-*/",
					" !\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String(
					"abcdefghijklmnopqrstuvwxyz0123456789_-*/",
					" !\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			}},
		},
		{
			name: "Full character set with vendor",
			kvs: []attribute.KeyValue{
				attribute.String(
					"abcdefghijklmnopqrstuvwxyz0123456789_-*/@a-z0-9_-*/",
					"!\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String(
					"abcdefghijklmnopqrstuvwxyz0123456789_-*/@a-z0-9_-*/",
					"!\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			}},
		},
		{
			name: "Full character with vendor starting with number",
			kvs: []attribute.KeyValue{
				attribute.String(
					"0123456789_-*/abcdefghijklmnopqrstuvwxyz@a-z0-9_-*/",
					"!\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String(
					"0123456789_-*/abcdefghijklmnopqrstuvwxyz@a-z0-9_-*/",
					"!\"#$%&'()*+-./0123456789:;<>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
				),
			}},
		},
		{
			name: "One field",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String("foo", "1"),
			}},
		},
		{
			name: "Two fields",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String("bar", "2"),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String("bar", "2"),
			}},
		},
		{
			name: "Long field key",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String(
					"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
					"1",
				),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String(
					"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
					"1",
				),
			}},
		},
		{
			name: "Long field key with vendor",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String(
					"ttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttt@vvvvvvvvvvvvvv",
					"1",
				),
			},
			expectedTraceState: TraceState{[]attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String(
					"ttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttt@vvvvvvvvvvvvvv",
					"1",
				),
			}},
		},
		{
			name: "Invalid whitespace value",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1 \t "),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Invalid whitespace key",
			kvs: []attribute.KeyValue{
				attribute.String(" \t bar", "2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Empty header value",
			kvs: []attribute.KeyValue{
				attribute.String("", ""),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Space in key",
			kvs: []attribute.KeyValue{
				attribute.String("foo ", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Capitalized key",
			kvs: []attribute.KeyValue{
				attribute.String("FOO", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Period in key",
			kvs: []attribute.KeyValue{
				attribute.String("foo.bar", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Empty vendor",
			kvs: []attribute.KeyValue{
				attribute.String("foo@", "1"),
				attribute.String("bar", "2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Empty key for vendor",
			kvs: []attribute.KeyValue{
				attribute.String("@foo", "1"),
				attribute.String("bar", "2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Double @",
			kvs: []attribute.KeyValue{
				attribute.String("foo@@bar", "1"),
				attribute.String("bar", "2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Compound vendor",
			kvs: []attribute.KeyValue{
				attribute.String("foo@bar@baz", "1"),
				attribute.String("bar", "2"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Key too long",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Key too long with vendor",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String("tttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttttt@v", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Vendor too long",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "1"),
				attribute.String("t@vvvvvvvvvvvvvvv", "1"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Equal sign in value",
			kvs: []attribute.KeyValue{
				attribute.String("foo", "bar=baz"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
		{
			name: "Empty value",
			kvs: []attribute.KeyValue{
				attribute.String("foo", ""),
				attribute.String("bar", "3"),
			},
			expectedTraceState: TraceState{},
			expectedErr:        errInvalidTraceStateKeyValue,
		},
	}

	messageFunc := func(kvs []attribute.KeyValue) []string {
		var out []string
		for _, kv := range kvs {
			out = append(out, fmt.Sprintf("%s=%s", kv.Key, kv.Value.AsString()))
		}
		return out
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := TraceStateFromKeyValues(tc.kvs...)
			if tc.expectedErr != nil {
				require.Error(t, err, messageFunc(tc.kvs))
				assert.Equal(t, TraceState{}, result)
				assert.Equal(t, tc.expectedErr, err)
			} else {
				require.NoError(t, err, messageFunc(tc.kvs))
				assert.NotNil(t, tc.expectedTraceState)
				assert.Equal(t, tc.expectedTraceState, result)
			}
		})
	}
}

func assertSpanContextEqual(got SpanContext, want SpanContext) bool {
	return got.spanID == want.spanID &&
		got.traceID == want.traceID &&
		got.traceFlags == want.traceFlags &&
		got.remote == want.remote &&
		assertTraceStateEqual(got.traceState, want.traceState)
}

func assertTraceStateEqual(got TraceState, want TraceState) bool {
	if len(got.kvs) != len(want.kvs) {
		return false
	}

	for i, kv := range got.kvs {
		if kv != want.kvs[i] {
			return false
		}
	}

	return true
}

var kvsWithMaxMembers = func() []attribute.KeyValue {
	kvs := make([]attribute.KeyValue, traceStateMaxListMembers)
	for i := 0; i < traceStateMaxListMembers; i++ {
		kvs[i] = attribute.String(fmt.Sprintf("key%d", i+1),
			fmt.Sprintf("value%d", i+1))
	}
	return kvs
}()

func TestNewSpanContext(t *testing.T) {
	testCases := []struct {
		name                string
		config              SpanContextConfig
		expectedSpanContext SpanContext
	}{
		{
			name: "Complete SpanContext",
			config: SpanContextConfig{
				TraceID:    TraceID([16]byte{1}),
				SpanID:     SpanID([8]byte{42}),
				TraceFlags: 0x1,
				TraceState: TraceState{kvs: []attribute.KeyValue{
					attribute.String("foo", "bar"),
				}},
			},
			expectedSpanContext: SpanContext{
				traceID:    TraceID([16]byte{1}),
				spanID:     SpanID([8]byte{42}),
				traceFlags: 0x1,
				traceState: TraceState{kvs: []attribute.KeyValue{
					attribute.String("foo", "bar"),
				}},
			},
		},
		{
			name:                "Empty SpanContext",
			config:              SpanContextConfig{},
			expectedSpanContext: SpanContext{},
		},
		{
			name: "Partial SpanContext",
			config: SpanContextConfig{
				TraceID: TraceID([16]byte{1}),
				SpanID:  SpanID([8]byte{42}),
			},
			expectedSpanContext: SpanContext{
				traceID:    TraceID([16]byte{1}),
				spanID:     SpanID([8]byte{42}),
				traceFlags: 0x0,
				traceState: TraceState{},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sctx := NewSpanContext(tc.config)
			if !assertSpanContextEqual(sctx, tc.expectedSpanContext) {
				t.Fatalf("%s: Unexpected context created: %s", tc.name, cmp.Diff(sctx, tc.expectedSpanContext))
			}
		})
	}
}

func TestSpanContextDerivation(t *testing.T) {
	from := SpanContext{}
	to := SpanContext{traceID: TraceID([16]byte{1})}

	modified := from.WithTraceID(to.TraceID())
	if !assertSpanContextEqual(modified, to) {
		t.Fatalf("WithTraceID: Unexpected context created: %s", cmp.Diff(modified, to))
	}

	from = to
	to.spanID = SpanID([8]byte{42})

	modified = from.WithSpanID(to.SpanID())
	if !assertSpanContextEqual(modified, to) {
		t.Fatalf("WithSpanID: Unexpected context created: %s", cmp.Diff(modified, to))
	}

	from = to
	to.traceFlags = 0x13

	modified = from.WithTraceFlags(to.TraceFlags())
	if !assertSpanContextEqual(modified, to) {
		t.Fatalf("WithTraceFlags: Unexpected context created: %s", cmp.Diff(modified, to))
	}

	from = to
	to.traceState = TraceState{kvs: []attribute.KeyValue{attribute.String("foo", "bar")}}

	modified = from.WithTraceState(to.TraceState())
	if !assertSpanContextEqual(modified, to) {
		t.Fatalf("WithTraceState: Unexpected context created: %s", cmp.Diff(modified, to))
	}
}
