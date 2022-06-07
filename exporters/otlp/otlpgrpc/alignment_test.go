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

package otlpgrpc

import (
	"os"
	"testing"
	"unsafe"

	ottest "go.opentelemetry.io/otel/internal/internaltest"
)

// Ensure struct alignment prior to running tests.
func TestMain(m *testing.M) {
	fields := []ottest.FieldOffset{
		{
			Name:   "connection.lastConnectErrPtr",
			Offset: unsafe.Offsetof(connection{}.lastConnectErrPtr),
		},
	}
	if !ottest.Aligned8Byte(fields, os.Stderr) {
		os.Exit(1)
	}

	os.Exit(m.Run())
}
