package webrtchls

import (
	"testing"
)

func TestInit(t *testing.T) {
	err := Init()
	if err != nil {
		t.Skipf("Library initialization failed (may not be available): %v", err)
	}
	defer Cleanup()
}

func TestContextCreation(t *testing.T) {
	err := Init()
	if err != nil {
		t.Skipf("Library initialization failed: %v", err)
	}
	defer Cleanup()

	ctx := NewContext()
	if ctx == nil {
		t.Fatal("Failed to create context")
	}
	defer ctx.Destroy()

	if ctx.handle == nil {
		t.Error("Context handle is nil")
	}
}

func TestSessionCreation(t *testing.T) {
	err := Init()
	if err != nil {
		t.Skipf("Library initialization failed: %v", err)
	}
	defer Cleanup()

	ctx := NewContext()
	if ctx == nil {
		t.Fatal("Failed to create context")
	}
	defer ctx.Destroy()

	config := &SessionConfig{
		SessionID:  "test-session",
		OutputPath: "/tmp/test-output.m3u8",
		EnableGst:  false,
	}

	session, err := ctx.CreateSession(config)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Destroy()

	if session.handle == nil {
		t.Error("Session handle is nil")
	}
}

func TestSessionCreationWithNilConfig(t *testing.T) {
	err := Init()
	if err != nil {
		t.Skipf("Library initialization failed: %v", err)
	}
	defer Cleanup()

	ctx := NewContext()
	if ctx == nil {
		t.Fatal("Failed to create context")
	}
	defer ctx.Destroy()

	_, err = ctx.CreateSession(nil)
	if err != ErrInvalidParam {
		t.Errorf("Expected ErrInvalidParam, got %v", err)
	}
}

func TestErrorConversion(t *testing.T) {
	tests := []struct {
		code     int
		expected error
	}{
		{0, nil},
		{-1, ErrGeneric},
		{-2, ErrNotFound},
		{-3, ErrAlreadyExists},
		{-4, ErrInvalidParam},
	}

	for _, tt := range tests {
		result := codeToError(tt.code)
		if result != tt.expected {
			t.Errorf("codeToError(%d) = %v, expected %v", tt.code, result, tt.expected)
		}
	}
}
