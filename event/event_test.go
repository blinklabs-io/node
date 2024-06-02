// Copyright 2024 Blink Labs Software
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

package event_test

import (
	"testing"
	"time"

	"github.com/blinklabs-io/node/event"
)

func TestEventBusSingleSubscriber(t *testing.T) {
	var testEvtData int = 999
	var testEvtType event.EventType = "test.event"
	eb := event.NewEventBus()
	_, subCh := eb.Subscribe(testEvtType)
	eb.Publish(testEvtType, event.NewEvent(testEvtType, testEvtData))
	select {
	case evt, ok := <-subCh:
		if !ok {
			t.Fatalf("event channel closed unexpectedly")
		}
		switch v := evt.Data.(type) {
		case int:
			if v != testEvtData {
				t.Fatalf("did not get expected event")
			}
		default:
			t.Fatalf("event data was not of expected type, expected int, got %T", evt.Data)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for event")
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	var testEvtData int = 999
	var testEvtType event.EventType = "test.event"
	eb := event.NewEventBus()
	_, sub1Ch := eb.Subscribe(testEvtType)
	_, sub2Ch := eb.Subscribe(testEvtType)
	eb.Publish(testEvtType, event.NewEvent(testEvtType, testEvtData))
	var gotVal1, gotVal2 bool
	for {
		if gotVal1 && gotVal2 {
			break
		}
		select {
		case evt, ok := <-sub1Ch:
			if !ok {
				t.Fatalf("event channel closed unexpectedly")
			}
			if gotVal1 {
				t.Fatalf("received unexpected event")
			}
			switch v := evt.Data.(type) {
			case int:
				if v != testEvtData {
					t.Fatalf("did not get expected event")
				}
			default:
				t.Fatalf("event data was not of expected type, expected int, got %T", evt.Data)
			}
			gotVal1 = true
		case evt, ok := <-sub2Ch:
			if !ok {
				t.Fatalf("event channel closed unexpectedly")
			}
			if gotVal2 {
				t.Fatalf("received unexpected event")
			}
			switch v := evt.Data.(type) {
			case int:
				if v != testEvtData {
					t.Fatalf("did not get expected event")
				}
			default:
				t.Fatalf("event data was not of expected type, expected int, got %T", evt.Data)
			}
			gotVal2 = true
		case <-time.After(1 * time.Second):
			t.Fatalf("timeout waiting for event")
		}
	}
}

func TestEventBusUnsubscribe(t *testing.T) {
	var testEvtData int = 999
	var testEvtType event.EventType = "test.event"
	eb := event.NewEventBus()
	subId, subCh := eb.Subscribe(testEvtType)
	eb.Unsubscribe(testEvtType, subId)
	eb.Publish(testEvtType, event.NewEvent(testEvtType, testEvtData))
	select {
	case _, ok := <-subCh:
		if !ok {
			t.Fatalf("event channel closed unexpectedly")
		}
		t.Fatalf("received unexpected event")
	case <-time.After(1 * time.Second):
		// NOTE: this is the expected way for the test to end
	}
}
