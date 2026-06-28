package platform

import (
	"errors"
	"testing"
)

func TestErrUnsupported(t *testing.T) {
	if !errors.Is(ErrUnsupported, ErrUnsupported) {
		t.Fatalf("ErrUnsupported should match itself")
	}
}

func TestCapabilitiesZeroValue(t *testing.T) {
	var caps Capabilities

	if caps.Text || caps.Typing || caps.Image || caps.File || caps.Card || caps.Streaming || caps.Buttons || caps.LongText {
		t.Fatalf("zero capabilities should disable every capability: %#v", caps)
	}
}
