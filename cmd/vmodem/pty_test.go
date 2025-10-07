package main

import (
	"testing"
)

// Test NewPty function
func TestNewPty(t *testing.T) {
	pty, err := NewPty()
	if err != nil {
		t.Fatalf("NewPty() error = %v, want nil", err)
	}
	defer pty.Close()

	if pty == nil {
		t.Fatal("NewPty() returned nil PTY")
	}

	// Check that master is valid
	if pty.Master() == nil {
		t.Error("Master() returned nil")
	}

	// Check that name is not empty
	name := pty.Name()
	if name == "" {
		t.Error("Name() returned empty string")
	}

	// Check that Fd returns valid file descriptor
	fd := pty.Fd()
	if fd == 0 {
		t.Error("Fd() returned invalid file descriptor")
	}
}

// Test PTY Close operation
func TestUnixPty_Close(t *testing.T) {
	pty, err := NewPty()
	if err != nil {
		t.Fatalf("NewPty() error = %v", err)
	}

	// Verify it's not closed initially
	if pty.closed {
		t.Error("PTY should not be closed initially")
	}

	// Close the PTY
	err = pty.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Verify it's marked as closed
	if !pty.closed {
		t.Error("PTY should be marked as closed")
	}

	// Second close should not error
	err = pty.Close()
	if err != nil {
		t.Errorf("Second Close() error = %v", err)
	}
}

// Test PTY basic read/write (simplified)
func TestUnixPty_BasicReadWrite(t *testing.T) {
	pty, err := NewPty()
	if err != nil {
		t.Fatalf("NewPty() error = %v", err)
	}
	defer pty.Close()

	// Test that Write doesn't immediately error
	testData := []byte("test")
	_, err = pty.Write(testData)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}
}
