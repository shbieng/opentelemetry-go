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

package global

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type errLogger []string

func (l *errLogger) Write(p []byte) (int, error) {
	msg := bytes.TrimRight(p, "\n")
	(*l) = append(*l, string(msg))
	return len(msg), nil
}

func (l *errLogger) Reset() {
	*l = errLogger([]string{})
}

func (l *errLogger) Got() []string {
	return []string(*l)
}

type HandlerTestSuite struct {
	suite.Suite

	origHandler *handler
	errLogger   *errLogger
}

func (s *HandlerTestSuite) SetupSuite() {
	s.errLogger = new(errLogger)
	s.origHandler = globalHandler
	globalHandler = &handler{
		l: log.New(s.errLogger, "", 0),
	}
}

func (s *HandlerTestSuite) TearDownSuite() {
	globalHandler = s.origHandler
}

func (s *HandlerTestSuite) SetupTest() {
	s.errLogger.Reset()
}

func (s *HandlerTestSuite) TestGlobalHandler() {
	errs := []string{"one", "two"}
	Handler().Handle(errors.New(errs[0]))
	Handle(errors.New(errs[1]))
	s.Assert().Equal(errs, s.errLogger.Got())
}

func (s *HandlerTestSuite) TestNoDropsOnDelegate() {
	// max time to wait for goroutine to Handle an error.
	pause := 10 * time.Millisecond

	var sent int
	err := errors.New("")
	stop := make(chan struct{})
	beat := make(chan struct{})
	done := make(chan struct{})

	// Wait for a error to be submitted from the following goroutine.
	wait := func(d time.Duration) error {
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
			// We are about to fail, stop the spawned goroutine.
			stop <- struct{}{}
			return fmt.Errorf("no errors sent in %v", d)
		case <-beat:
			timer.Stop()
			return nil
		}
	}

	go func() {
		for {
			select {
			case <-stop:
				done <- struct{}{}
				return
			default:
				sent++
				Handle(err)
			}

			select {
			case beat <- struct{}{}:
			default:
			}
		}
	}()

	// Wait for the spice to flow
	s.Require().NoError(wait(pause), "starting error stream")

	// Change to another Handler. We are testing this is loss-less.
	newErrLogger := new(errLogger)
	secondary := &handler{
		l: log.New(newErrLogger, "", 0),
	}
	SetHandler(secondary)
	// Flush so we can ensure new errors are sent to new Handler.
	s.Require().NoError(wait(pause), "flushing error stream")
	// Now beat is clear, wait for a fresh send.
	s.Require().NoError(wait(pause), "getting fresh errors to new Handler")

	// Testing done, stop sending errors.
	stop <- struct{}{}
	// Ensure we do not lose any straglers.
	<-done

	got := append(s.errLogger.Got(), newErrLogger.Got()...)
	s.Assert().Greater(len(got), 1, "at least 2 errors should have been sent")
	s.Assert().Len(got, sent)
}

func TestHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(HandlerTestSuite))
}
