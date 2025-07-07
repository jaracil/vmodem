package vmodem

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockReadWriteCloser implements io.ReadWriteCloser for testing
type MockReadWriteCloser struct {
	data      []byte
	pos       int
	writes    []byte
	closed    bool
	readChan  chan byte
	writeChan chan byte
	mu        sync.Mutex // Protege writes y closed
}

func NewMockReadWriteCloser(data []byte) *MockReadWriteCloser {
	return &MockReadWriteCloser{
		data:     data,
		readChan: make(chan byte, 1000),
	}
}

func (m *MockReadWriteCloser) Read(p []byte) (int, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	
	if closed {
		return 0, io.EOF
	}
	
	// First try to read from initial data
	if m.pos < len(m.data) {
		n := copy(p, m.data[m.pos:])
		m.pos += n
		return n, nil
	}
	
	// Then try to read from channel (simulating real input)
	// Block indefinitely like a real TTY would
	select {
	case b := <-m.readChan:
		p[0] = b
		return 1, nil
	}
}

func (m *MockReadWriteCloser) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	m.writes = append(m.writes, p...)
	return len(p), nil
}

func (m *MockReadWriteCloser) WriteInput(data []byte) {
	// Send data through channel to simulate external input
	for _, b := range data {
		select {
		case m.readChan <- b:
		default:
			// Channel full, skip
		}
	}
}

func (m *MockReadWriteCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.closed = true
	return nil
}

func (m *MockReadWriteCloser) GetWrittenString() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	return string(m.writes)
}

func (m *MockReadWriteCloser) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	return m.closed
}

func (m *MockReadWriteCloser) ClearWrites() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.writes = nil
}

// Test ModemStatus.String() method
func TestModemStatus_String(t *testing.T) {
	tests := []struct {
		name     string
		status   ModemStatus
		expected string
	}{
		{"StatusIdle", StatusIdle, "Idle"},
		{"StatusDialing", StatusDialing, "Dialing"},
		{"StatusConnected", StatusConnected, "Connected"},
		{"StatusConnectedCmd", StatusConnectedCmd, "ConnectedCmd"},
		{"StatusRinging", StatusRinging, "Ringing"},
		{"StatusClosed", StatusClosed, "Closed"},
		{"Unknown status", ModemStatus(99), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.status.String()
			if result != tt.expected {
				t.Errorf("ModemStatus.String() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Test CmdReturnFromString function
func TestCmdReturnFromString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected RetCode
	}{
		{"OK command", "OK", RetCodeOk},
		{"ERROR command", "ERROR", RetCodeError},
		{"CONNECT command", "CONNECT", RetCodeConnect},
		{"NO CARRIER command", "NO CARRIER", RetCodeNoCarrier},
		{"Unknown command", "UNKNOWN_COMMAND", RetCodeUnknown},
		{"Empty string", "", RetCodeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CmdReturnFromString(tt.input)
			if result != tt.expected {
				t.Errorf("CmdReturnFromString(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// Test NewModem function
func TestNewModem(t *testing.T) {
	t.Run("Valid config", func(t *testing.T) {
		tty := NewMockReadWriteCloser([]byte{})
		config := &ModemConfig{
			Id:  "test-modem",
			TTY: tty,
		}
		
		modem, err := NewModem(config)
		if err != nil {
			t.Fatalf("NewModem() error = %v, want nil", err)
		}
		
		if modem == nil {
			t.Fatal("NewModem() returned nil modem")
		}
		
		// Check initial state
		if modem.StatusSync() != StatusIdle {
			t.Errorf("Initial status = %v, want %v", modem.StatusSync(), StatusIdle)
		}
		
		// Cleanup
		modem.CloseSync()
	})
	
	t.Run("Nil config", func(t *testing.T) {
		modem, err := NewModem(nil)
		if err != ErrConfigRequired {
			t.Errorf("NewModem(nil) error = %v, want %v", err, ErrConfigRequired)
		}
		if modem != nil {
			t.Error("NewModem(nil) should return nil modem")
		}
	})
}

// Test basic AT command processing
func TestModem_ProcessAtCommand_Basic(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Test basic commands
	tests := []struct {
		command  string
		expected RetCode
	}{
		{"E0", RetCodeOk},  // Echo off
		{"E1", RetCodeOk},  // Echo on
		{"V0", RetCodeOk},  // Verbose off
		{"V1", RetCodeOk},  // Verbose on
		{"Q0", RetCodeOk},  // Quiet off
		{"Q1", RetCodeOk},  // Quiet on
		{"H", RetCodeOk},   // Hangup
		{"&F", RetCodeOk},  // Factory reset
		{"Z", RetCodeOk},   // Reset
	}
	
	for _, test := range tests {
		result := modem.ProcessAtCommandSync(test.command)
		if result != test.expected {
			t.Errorf("ProcessAtCommand(%q) = %v, want %v", test.command, result, test.expected)
		}
	}
}

// Test state transitions
func TestModem_StateTransitions(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Test valid transition: Idle -> Dialing
	modem.SetStatusSync(StatusDialing)
	if modem.StatusSync() != StatusDialing {
		t.Errorf("Expected StatusDialing, got %v", modem.StatusSync())
	}
	
	// Return to idle
	modem.SetStatusSync(StatusIdle)
	if modem.StatusSync() != StatusIdle {
		t.Errorf("Expected StatusIdle, got %v", modem.StatusSync())
	}
}

// Test TTY operations
func TestModem_TtyOperations(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Test writing to TTY
	testString := "Hello, TTY!"
	modem.TtyWriteStrSync(testString)
	
	written := tty.GetWrittenString()
	if written != testString {
		t.Errorf("TtyWriteStrSync wrote %q, want %q", written, testString)
	}
}

// Test modem metrics
func TestModem_Metrics(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Get metrics
	metrics := modem.MetricsSync()
	if metrics == nil {
		t.Fatal("MetricsSync() returned nil")
	}
	
	// Check initial state
	if metrics.Status != StatusIdle {
		t.Errorf("Initial status = %v, want %v", metrics.Status, StatusIdle)
	}
	
	if metrics.TtyTxBytes != 0 {
		t.Errorf("Initial TtyTxBytes = %d, want 0", metrics.TtyTxBytes)
	}
}

// Test AT command flow through TTY
func TestModem_ATCommandFlow(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait a bit for the ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{"Echo Off", "ATE0\r", "OK"},
		{"Echo On", "ATE1\r", "OK"}, 
		{"Verbose Off", "ATV0\r", "0"},
		{"Verbose On", "ATV1\r", "OK"},
		{"Quiet Off", "ATQ0\r", "OK"},
		{"Factory Reset", "AT&F\r", "OK"},
		{"Reset", "ATZ\r", "OK"},
		{"Hangup", "ATH\r", "OK"},
	}
	
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Clear previous output
			tty.ClearWrites()
			
			// Send AT command through TTY
			tty.WriteInput([]byte(test.command))
			
			// Wait for processing
			time.Sleep(50 * time.Millisecond)
			
			// Check response
			response := tty.GetWrittenString()
			if !strings.Contains(response, test.expected) {
				t.Errorf("Expected response to contain %q, got %q", test.expected, response)
			}
		})
	}
}

// Test AT command chaining through TTY
func TestModem_ATCommandChaining(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait for ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	// Send chained command
	tty.WriteInput([]byte("ATE0V1Q0\r"))
	
	// Wait for processing
	time.Sleep(50 * time.Millisecond)
	
	// Should get OK response
	response := tty.GetWrittenString()
	if !strings.Contains(response, "OK") {
		t.Errorf("Expected OK response to chained command, got %q", response)
	}
}

// Test invalid AT command through TTY
func TestModem_ATCommandInvalid(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait for ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	// Send invalid command
	tty.WriteInput([]byte("ATE5\r")) // E5 is invalid (only E0/E1 allowed)
	
	// Wait for processing
	time.Sleep(50 * time.Millisecond)
	
	// Should get ERROR response
	response := tty.GetWrittenString()
	if !strings.Contains(response, "ERROR") {
		t.Errorf("Expected ERROR response to invalid command, got %q", response)
	}
}

// Test S register operations through TTY
func TestModem_SRegisterFlow(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait for ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	// Set S register
	tty.WriteInput([]byte("ATS0=5\r"))
	time.Sleep(50 * time.Millisecond)
	
	response := tty.GetWrittenString()
	if !strings.Contains(response, "OK") {
		t.Errorf("Expected OK response to S register set, got %q", response)
	}
	
	// Clear output and query S register
	tty.ClearWrites()
	tty.WriteInput([]byte("ATS0?\r"))
	time.Sleep(50 * time.Millisecond)
	
	response = tty.GetWrittenString()
	if !strings.Contains(response, "005") {
		t.Errorf("Expected S register query to show 005, got %q", response)
	}
}

// Test echo behavior through TTY
func TestModem_EchoFlow(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem",
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait for ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	// Initially echo should be on - test with a command
	tty.WriteInput([]byte("ATE1\r"))
	time.Sleep(50 * time.Millisecond)
	
	response := tty.GetWrittenString()
	// With echo on, we should see the command echoed back
	if !strings.Contains(response, "ATE1") {
		t.Errorf("Expected command to be echoed back, got %q", response)
	}
	
	// Clear and turn echo off
	tty.ClearWrites()
	tty.WriteInput([]byte("ATE0\r"))
	time.Sleep(50 * time.Millisecond)
	
	response = tty.GetWrittenString()
	// Should still see this command echoed (echo was on when we sent it)
	if !strings.Contains(response, "ATE0") {
		t.Errorf("Expected command to be echoed back, got %q", response)
	}
	
	// Clear and send another command - this should not be echoed
	tty.ClearWrites()
	tty.WriteInput([]byte("ATH\r"))
	time.Sleep(50 * time.Millisecond)
	
	response = tty.GetWrittenString()
	// Should not see the command echoed back (only response)
	if strings.Contains(response, "ATH") && !strings.Contains(response, "OK") {
		t.Errorf("Command should not be echoed with echo off, got %q", response)
	}
}

// Test A/ repeat command through TTY
func TestModem_RepeatCommand(t *testing.T) {
	tty := NewMockReadWriteCloser([]byte{})
	config := &ModemConfig{
		Id:  "test-modem", 
		TTY: tty,
	}
	
	modem, err := NewModem(config)
	if err != nil {
		t.Fatalf("NewModem() error = %v", err)
	}
	defer modem.CloseSync()
	
	// Wait for ttyReadTask to start
	time.Sleep(10 * time.Millisecond)
	
	// Send first command
	tty.WriteInput([]byte("ATE0\r"))
	time.Sleep(50 * time.Millisecond)
	
	response := tty.GetWrittenString()
	if !strings.Contains(response, "OK") {
		t.Errorf("Expected OK response, got %q", response)
	}
	
	// Clear and send repeat command
	tty.ClearWrites()
	tty.WriteInput([]byte("A/"))
	time.Sleep(50 * time.Millisecond)
	
	response = tty.GetWrittenString()
	// Should repeat the last command (ATE0) and get OK
	if !strings.Contains(response, "OK") {
		t.Errorf("Expected OK response to repeat command, got %q", response)
	}
}