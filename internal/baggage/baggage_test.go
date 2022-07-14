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

package baggage

import (
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

type testCase struct {
	name    string
	value   MapUpdate
	init    []int
	wantKVs []attribute.KeyValue
}

func TestMap(t *testing.T) {
	for _, testcase := range getTestCases() {
		t.Logf("Running test case %s", testcase.name)
		var got Map
		if len(testcase.init) > 0 {
			got = makeTestMap(testcase.init).Apply(testcase.value)
		} else {
			got = NewMap(testcase.value)
		}
		for _, s := range testcase.wantKVs {
			if ok := got.HasValue(s.Key); !ok {
				t.Errorf("Expected Key %s to have Value", s.Key)
			}
			if g, ok := got.Value(s.Key); !ok || g != s.Value {
				t.Errorf("+got: %v, -want: %v", g, s.Value)
			}
		}
		// test Foreach()
		got.Foreach(func(kv attribute.KeyValue) bool {
			for _, want := range testcase.wantKVs {
				if kv == want {
					return false
				}
			}
			t.Errorf("Expected label %v, but not found", kv)
			return true
		})
		if l, exp := got.Len(), len(testcase.wantKVs); l != exp {
			t.Errorf("+got: %d, -want: %d", l, exp)
		}
	}
}

func TestSizeComputation(t *testing.T) {
	for _, testcase := range getTestCases() {
		t.Logf("Running test case %s", testcase.name)
		var initMap Map
		if len(testcase.init) > 0 {
			initMap = makeTestMap(testcase.init)
		} else {
			initMap = NewEmptyMap()
		}
		gotMap := initMap.Apply(testcase.value)

		delSet, addSet := getModificationSets(testcase.value)
		mapSize := getNewMapSize(initMap.m, delSet, addSet)

		if gotMap.Len() != mapSize {
			t.Errorf("Expected computed size to be %d, got %d", gotMap.Len(), mapSize)
		}
	}
}

func getTestCases() []testCase {
	return []testCase{
		{
			name: "map with MultiKV",
			value: MapUpdate{MultiKV: []attribute.KeyValue{
				attribute.Int64("key1", 1),
				attribute.String("key2", "val2")},
			},
			init: []int{},
			wantKVs: []attribute.KeyValue{
				attribute.Int64("key1", 1),
				attribute.String("key2", "val2"),
			},
		},
		{
			name:  "map with SingleKV",
			value: MapUpdate{SingleKV: attribute.String("key1", "val1")},
			init:  []int{},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", "val1"),
			},
		},
		{
			name: "map with both add fields",
			value: MapUpdate{SingleKV: attribute.Int64("key1", 3),
				MultiKV: []attribute.KeyValue{
					attribute.String("key1", ""),
					attribute.String("key2", "val2")},
			},
			init: []int{},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", ""),
				attribute.String("key2", "val2"),
			},
		},
		{
			name:    "map with empty MapUpdate",
			value:   MapUpdate{},
			init:    []int{},
			wantKVs: []attribute.KeyValue{},
		},
		{
			name:    "map with DropSingleK",
			value:   MapUpdate{DropSingleK: attribute.Key("key1")},
			init:    []int{},
			wantKVs: []attribute.KeyValue{},
		},
		{
			name: "map with DropMultiK",
			value: MapUpdate{DropMultiK: []attribute.Key{
				attribute.Key("key1"), attribute.Key("key2"),
			}},
			init:    []int{},
			wantKVs: []attribute.KeyValue{},
		},
		{
			name: "map with both drop fields",
			value: MapUpdate{
				DropSingleK: attribute.Key("key1"),
				DropMultiK: []attribute.Key{
					attribute.Key("key1"),
					attribute.Key("key2"),
				},
			},
			init:    []int{},
			wantKVs: []attribute.KeyValue{},
		},
		{
			name: "map with all fields",
			value: MapUpdate{
				DropSingleK: attribute.Key("key1"),
				DropMultiK: []attribute.Key{
					attribute.Key("key1"),
					attribute.Key("key2"),
				},
				SingleKV: attribute.String("key4", "val4"),
				MultiKV: []attribute.KeyValue{
					attribute.String("key1", ""),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			init: []int{},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", ""),
				attribute.String("key2", "val2"),
				attribute.String("key3", "val3"),
				attribute.String("key4", "val4"),
			},
		},
		{
			name: "Existing map with MultiKV",
			value: MapUpdate{MultiKV: []attribute.KeyValue{
				attribute.Int64("key1", 1),
				attribute.String("key2", "val2")},
			},
			init: []int{5},
			wantKVs: []attribute.KeyValue{
				attribute.Int64("key1", 1),
				attribute.String("key2", "val2"),
				attribute.Int("key5", 5),
			},
		},
		{
			name:  "Existing map with SingleKV",
			value: MapUpdate{SingleKV: attribute.String("key1", "val1")},
			init:  []int{5},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", "val1"),
				attribute.Int("key5", 5),
			},
		},
		{
			name: "Existing map with both add fields",
			value: MapUpdate{SingleKV: attribute.Int64("key1", 3),
				MultiKV: []attribute.KeyValue{
					attribute.String("key1", ""),
					attribute.String("key2", "val2")},
			},
			init: []int{5},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", ""),
				attribute.String("key2", "val2"),
				attribute.Int("key5", 5),
			},
		},
		{
			name:  "Existing map with empty MapUpdate",
			value: MapUpdate{},
			init:  []int{5},
			wantKVs: []attribute.KeyValue{
				attribute.Int("key5", 5),
			},
		},
		{
			name:  "Existing map with DropSingleK",
			value: MapUpdate{DropSingleK: attribute.Key("key1")},
			init:  []int{1, 5},
			wantKVs: []attribute.KeyValue{
				attribute.Int("key5", 5),
			},
		},
		{
			name: "Existing map with DropMultiK",
			value: MapUpdate{DropMultiK: []attribute.Key{
				attribute.Key("key1"), attribute.Key("key2"),
			}},
			init: []int{1, 5},
			wantKVs: []attribute.KeyValue{
				attribute.Int("key5", 5),
			},
		},
		{
			name: "Existing map with both drop fields",
			value: MapUpdate{
				DropSingleK: attribute.Key("key1"),
				DropMultiK: []attribute.Key{
					attribute.Key("key1"),
					attribute.Key("key2"),
				},
			},
			init: []int{1, 2, 5},
			wantKVs: []attribute.KeyValue{
				attribute.Int("key5", 5),
			},
		},
		{
			name: "Existing map with all the fields",
			value: MapUpdate{
				DropSingleK: attribute.Key("key1"),
				DropMultiK: []attribute.Key{
					attribute.Key("key1"),
					attribute.Key("key2"),
					attribute.Key("key5"),
					attribute.Key("key6"),
				},
				SingleKV: attribute.String("key4", "val4"),
				MultiKV: []attribute.KeyValue{
					attribute.String("key1", ""),
					attribute.String("key2", "val2"),
					attribute.String("key3", "val3"),
				},
			},
			init: []int{5, 6, 7},
			wantKVs: []attribute.KeyValue{
				attribute.String("key1", ""),
				attribute.String("key2", "val2"),
				attribute.String("key3", "val3"),
				attribute.String("key4", "val4"),
				attribute.Int("key7", 7),
			},
		},
	}
}

func makeTestMap(ints []int) Map {
	r := make(rawMap, len(ints))
	for _, v := range ints {
		r[attribute.Key(fmt.Sprintf("key%d", v))] = attribute.IntValue(v)
	}
	return newMap(r)
}
