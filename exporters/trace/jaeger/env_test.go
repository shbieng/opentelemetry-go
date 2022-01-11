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

package jaeger

import (
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ottest "go.opentelemetry.io/otel/internal/testing"
	"go.opentelemetry.io/otel/label"
)

func Test_parseTags(t *testing.T) {
	envStore, err := ottest.SetEnvVariables(map[string]string{
		"existing": "not-default",
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()

	testCases := []struct {
		name          string
		tagStr        string
		expectedTags  []label.KeyValue
		expectedError error
	}{
		{
			name:   "string",
			tagStr: "key=value",
			expectedTags: []label.KeyValue{
				{
					Key:   "key",
					Value: label.StringValue("value"),
				},
			},
		},
		{
			name:   "int64",
			tagStr: "k=9223372036854775807,k2=-9223372036854775808",
			expectedTags: []label.KeyValue{
				{
					Key:   "k",
					Value: label.Int64Value(math.MaxInt64),
				},
				{
					Key:   "k2",
					Value: label.Int64Value(math.MinInt64),
				},
			},
		},
		{
			name:   "float64",
			tagStr: "k=1.797693134862315708145274237317043567981e+308,k2=4.940656458412465441765687928682213723651e-324,k3=-1.2",
			expectedTags: []label.KeyValue{
				{
					Key:   "k",
					Value: label.Float64Value(math.MaxFloat64),
				},
				{
					Key:   "k2",
					Value: label.Float64Value(math.SmallestNonzeroFloat64),
				},
				{
					Key:   "k3",
					Value: label.Float64Value(-1.2),
				},
			},
		},
		{
			name:   "multiple type values",
			tagStr: "k=v,k2=123, k3=v3 ,k4=-1.2, k5=${existing:default},k6=${nonExisting:default}",
			expectedTags: []label.KeyValue{
				{
					Key:   "k",
					Value: label.StringValue("v"),
				},
				{
					Key:   "k2",
					Value: label.Int64Value(123),
				},
				{
					Key:   "k3",
					Value: label.StringValue("v3"),
				},
				{
					Key:   "k4",
					Value: label.Float64Value(-1.2),
				},
				{
					Key:   "k5",
					Value: label.StringValue("not-default"),
				},
				{
					Key:   "k6",
					Value: label.StringValue("default"),
				},
			},
		},
		{
			name:          "malformed: only have key",
			tagStr:        "key",
			expectedError: errTagValueNotFound,
		},
		{
			name:          "malformed: environment key has no default value",
			tagStr:        "key=${foo}",
			expectedError: errTagEnvironmentDefaultValueNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tags, err := parseTags(tc.tagStr)
			if tc.expectedError == nil {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedTags, tags)
			} else {
				assert.Error(t, err)
				assert.Equal(t, tc.expectedError, err)
				assert.Equal(t, tc.expectedTags, tags)
			}
		})
	}
}

func Test_parseValue(t *testing.T) {
	testCases := []struct {
		name     string
		str      string
		expected label.Value
	}{
		{
			name:     "bool: true",
			str:      "true",
			expected: label.BoolValue(true),
		},
		{
			name:     "bool: false",
			str:      "false",
			expected: label.BoolValue(false),
		},
		{
			name:     "int64: 012340",
			str:      "012340",
			expected: label.Int64Value(12340),
		},
		{
			name:     "int64: -012340",
			str:      "-012340",
			expected: label.Int64Value(-12340),
		},
		{
			name:     "int64: 0",
			str:      "0",
			expected: label.Int64Value(0),
		},
		{
			name:     "float64: -0.1",
			str:      "-0.1",
			expected: label.Float64Value(-0.1),
		},
		{
			name:     "float64: 00.001",
			str:      "00.001",
			expected: label.Float64Value(0.001),
		},
		{
			name:     "float64: 1E23",
			str:      "1E23",
			expected: label.Float64Value(1e23),
		},
		{
			name:     "string: foo",
			str:      "foo",
			expected: label.StringValue("foo"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := parseValue(tc.str)
			assert.Equal(t, tc.expected, v)
		})
	}
}

func TestNewRawExporterWithEnv(t *testing.T) {
	const (
		collectorEndpoint = "http://localhost"
		username          = "user"
		password          = "password"
		serviceName       = "test-service"
		disabled          = "false"
		tags              = "key=value"
	)

	envStore, err := ottest.SetEnvVariables(map[string]string{
		envEndpoint:    collectorEndpoint,
		envUser:        username,
		envPassword:    password,
		envDisabled:    disabled,
		envServiceName: serviceName,
		envTags:        tags,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()

	// Create Jaeger Exporter with environment variables
	exp, err := NewRawExporter(
		WithCollectorEndpoint(CollectorEndpointFromEnv(), WithCollectorEndpointOptionFromEnv()),
		WithDisabled(true),
		WithDisabledFromEnv(),
		WithProcessFromEnv(),
	)

	assert.NoError(t, err)
	assert.Equal(t, false, exp.o.Disabled)
	assert.EqualValues(t, serviceName, exp.process.ServiceName)
	assert.Len(t, exp.process.Tags, 1)

	require.IsType(t, &collectorUploader{}, exp.uploader)
	uploader := exp.uploader.(*collectorUploader)
	assert.Equal(t, collectorEndpoint, uploader.endpoint)
	assert.Equal(t, username, uploader.username)
	assert.Equal(t, password, uploader.password)
}

func TestNewRawExporterWithEnvImplicitly(t *testing.T) {
	const (
		collectorEndpoint = "http://localhost"
		username          = "user"
		password          = "password"
		serviceName       = "test-service"
		disabled          = "false"
		tags              = "key=value"
	)

	envStore, err := ottest.SetEnvVariables(map[string]string{
		envEndpoint:    collectorEndpoint,
		envUser:        username,
		envPassword:    password,
		envDisabled:    disabled,
		envServiceName: serviceName,
		envTags:        tags,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()

	// Create Jaeger Exporter with environment variables
	exp, err := NewRawExporter(
		WithCollectorEndpoint("should be overwritten"),
		WithDisabled(true),
	)

	assert.NoError(t, err)
	// NewRawExporter will ignore Disabled env
	assert.Equal(t, true, exp.o.Disabled)
	assert.EqualValues(t, serviceName, exp.process.ServiceName)
	assert.Len(t, exp.process.Tags, 1)

	require.IsType(t, &collectorUploader{}, exp.uploader)
	uploader := exp.uploader.(*collectorUploader)
	assert.Equal(t, collectorEndpoint, uploader.endpoint)
	assert.Equal(t, username, uploader.username)
	assert.Equal(t, password, uploader.password)
}

func TestCollectorEndpointFromEnv(t *testing.T) {
	const (
		collectorEndpoint = "http://localhost"
	)

	envStore, err := ottest.SetEnvVariables(map[string]string{
		envEndpoint: collectorEndpoint,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()

	assert.Equal(t, collectorEndpoint, CollectorEndpointFromEnv())
}

func TestWithCollectorEndpointOptionFromEnv(t *testing.T) {
	testCases := []struct {
		name                             string
		envUsername                      string
		envPassword                      string
		collectorEndpointOptions         CollectorEndpointOptions
		expectedCollectorEndpointOptions CollectorEndpointOptions
	}{
		{
			name:        "overrides value via environment variables",
			envUsername: "username",
			envPassword: "password",
			collectorEndpointOptions: CollectorEndpointOptions{
				username: "foo",
				password: "bar",
			},
			expectedCollectorEndpointOptions: CollectorEndpointOptions{
				username: "username",
				password: "password",
			},
		},
		{
			name:        "environment variables is empty, will not overwrite value",
			envUsername: "",
			envPassword: "",
			collectorEndpointOptions: CollectorEndpointOptions{
				username: "foo",
				password: "bar",
			},
			expectedCollectorEndpointOptions: CollectorEndpointOptions{
				username: "foo",
				password: "bar",
			},
		},
	}

	envStore := ottest.NewEnvStore()
	envStore.Record(envUser)
	envStore.Record(envPassword)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.Setenv(envUser, tc.envUsername))
			require.NoError(t, os.Setenv(envPassword, tc.envPassword))

			f := WithCollectorEndpointOptionFromEnv()
			f(&tc.collectorEndpointOptions)

			assert.Equal(t, tc.expectedCollectorEndpointOptions, tc.collectorEndpointOptions)
		})
	}
}

func TestWithDisabledFromEnv(t *testing.T) {
	testCases := []struct {
		name            string
		env             string
		options         options
		expectedOptions options
	}{
		{
			name:            "overwriting",
			env:             "true",
			options:         options{},
			expectedOptions: options{Disabled: true},
		},
		{
			name:            "no overwriting",
			env:             "",
			options:         options{Disabled: true},
			expectedOptions: options{Disabled: true},
		},
	}

	envStore := ottest.NewEnvStore()
	envStore.Record(envDisabled)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.Setenv(envDisabled, tc.env))

			f := WithDisabledFromEnv()
			f(&tc.options)

			assert.Equal(t, tc.expectedOptions, tc.options)
		})
	}
}

func TestProcessFromEnv(t *testing.T) {
	testCases := []struct {
		name            string
		serviceName     string
		tags            string
		expectedProcess Process
	}{
		{
			name:        "set process",
			serviceName: "test-service",
			tags:        "key=value,key2=123",
			expectedProcess: Process{
				ServiceName: "test-service",
				Tags: []label.KeyValue{
					label.String("key", "value"),
					label.Int64("key2", 123),
				},
			},
		},
		{
			name:        "malformed tags",
			serviceName: "test-service",
			tags:        "key",
			expectedProcess: Process{
				ServiceName: "test-service",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			envStore, err := ottest.SetEnvVariables(map[string]string{
				envServiceName: tc.serviceName,
				envTags:        tc.tags,
			})
			require.NoError(t, err)

			p := ProcessFromEnv()
			assert.Equal(t, tc.expectedProcess, p)

			require.NoError(t, envStore.Restore())
		})
	}
}

func TestWithProcessFromEnv(t *testing.T) {
	testCases := []struct {
		name            string
		envServiceName  string
		envTags         string
		options         options
		expectedOptions options
	}{
		{
			name:           "overwriting",
			envServiceName: "service-name",
			envTags:        "key=value",
			options: options{
				Process: Process{
					ServiceName: "old-name",
					Tags: []label.KeyValue{
						label.String("old-key", "old-value"),
					},
				},
			},
			expectedOptions: options{
				Process: Process{
					ServiceName: "service-name",
					Tags: []label.KeyValue{
						label.String("key", "value"),
					},
				},
			},
		},
		{
			name:           "no overwriting",
			envServiceName: "",
			envTags:        "",
			options: options{
				Process: Process{
					ServiceName: "old-name",
					Tags: []label.KeyValue{
						label.String("old-key", "old-value"),
					},
				},
			},
			expectedOptions: options{
				Process: Process{
					ServiceName: "old-name",
					Tags: []label.KeyValue{
						label.String("old-key", "old-value"),
					},
				},
			},
		},
	}

	envStore := ottest.NewEnvStore()
	envStore.Record(envServiceName)
	envStore.Record(envTags)
	defer func() {
		require.NoError(t, envStore.Restore())
	}()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.Setenv(envServiceName, tc.envServiceName))
			require.NoError(t, os.Setenv(envTags, tc.envTags))

			f := WithProcessFromEnv()
			f(&tc.options)

			assert.Equal(t, tc.expectedOptions, tc.options)
		})
	}
}
