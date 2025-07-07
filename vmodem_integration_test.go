package vmodem

import (
	"io"
	"strings"
	"testing"
	"time"
)

// MockConnection simulates a bidirectional network connection
type MockConnection struct {
	readData  []byte
	writeData []byte
	closed    bool
	peer      *MockConnection
}

func NewMockConnection() (*MockConnection, *MockConnection) {
	conn1 := &MockConnection{}
	conn2 := &MockConnection{}
	
	conn1.peer = conn2
	conn2.peer = conn1
	
	return conn1, conn2
}

func (c *MockConnection) Read(p []byte) (int, error) {
	if c.closed {
		return 0, io.EOF
	}
	
	// Block waiting for data indefinitely (like a real connection would)
	for len(c.readData) == 0 && !c.closed {
		time.Sleep(10 * time.Millisecond)
	}
	
	if c.closed {
		return 0, io.EOF
	}
	
	if len(c.readData) == 0 {
		return 0, io.EOF
	}
	
	n := copy(p, c.readData)
	c.readData = c.readData[n:]
	return n, nil
}

func (c *MockConnection) Write(p []byte) (int, error) {
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	
	// Write to peer's read buffer
	if c.peer != nil {
		c.peer.readData = append(c.peer.readData, p...)
	}
	// Also keep track of what we wrote
	c.writeData = append(c.writeData, p...)
	
	return len(p), nil
}

func (c *MockConnection) Close() error {
	c.closed = true
	// Signal peer that connection is closed by making its reads return EOF
	if c.peer != nil {
		c.peer.closed = true
	}
	return nil
}

// Test communication between two modem instances
func TestModem_InterModemCommunication(t *testing.T) {
	// Create TTYs for both modems
	callerTTY := NewMockReadWriteCloser([]byte{})
	answererTTY := NewMockReadWriteCloser([]byte{})
	
	// Create mock network connection between modems
	callerConn, answererConn := NewMockConnection()
	
	// Create outgoing call handler for caller modem
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		if number == "12345" {
			return callerConn, nil
		}
		return nil, ErrNoCarrier
	}
	
	// Create caller modem
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
		ConnectStr:   "CONNECT",
		AnswerChar:   "C",
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	// Create answerer modem  
	answererConfig := &ModemConfig{
		Id:         "answerer",
		TTY:        answererTTY,
		ConnectStr: "CONNECT",
		AnswerChar: "C",
	}
	
	answerer, err := NewModem(answererConfig)
	if err != nil {
		t.Fatalf("Failed to create answerer modem: %v", err)
	}
	defer answerer.CloseSync()
	
	// Wait for modems to initialize
	time.Sleep(20 * time.Millisecond)
	
	t.Logf("Initial states - Caller: %v, Answerer: %v", caller.StatusSync(), answerer.StatusSync())
	
	// Set up answerer to receive incoming call
	err = answerer.IncomingCallSync(answererConn)
	if err != nil {
		t.Fatalf("Failed to set up incoming call: %v", err)
	}
	
	// Verify answerer is ringing
	answererStatus := answerer.StatusSync()
	t.Logf("Answerer status after incoming call: %v", answererStatus)
	if answererStatus != StatusRinging {
		t.Errorf("Answerer should be ringing, got %v", answererStatus)
	}
	
	// Caller dials the number
	callerTTY.ClearWrites()
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	
	// Wait for dialing to start
	time.Sleep(50 * time.Millisecond)
	
	// Verify caller is dialing
	callerStatus := caller.StatusSync()
	t.Logf("Caller status after dial: %v", callerStatus)
	if callerStatus != StatusDialing && callerStatus != StatusConnected {
		t.Errorf("Caller should be dialing or connected, got %v", callerStatus)
	}
	
	// Answerer answers the call
	answererTTY.ClearWrites()
	answererTTY.WriteInput([]byte("ATA\r"))
	
	// Wait for connection to establish
	time.Sleep(200 * time.Millisecond)
	
	// Check intermediate states
	callerStatus = caller.StatusSync()
	answererStatus = answerer.StatusSync()
	t.Logf("Intermediate states after answer - Caller: %v, Answerer: %v", callerStatus, answererStatus)
	
	// Wait a bit more to see if states stabilize
	time.Sleep(100 * time.Millisecond)
	
	// Check final states
	callerFinalStatus := caller.StatusSync()
	answererFinalStatus := answerer.StatusSync()
	t.Logf("Final states - Caller: %v, Answerer: %v", callerFinalStatus, answererFinalStatus)
	
	// Verify both modems are connected
	if callerFinalStatus != StatusConnected {
		t.Errorf("Caller should be connected, got %v", callerFinalStatus)
		
		// Debug: check what was written to caller TTY
		callerOutput := callerTTY.GetWrittenString()
		t.Logf("Caller TTY output: %q", callerOutput)
	}
	
	if answererFinalStatus != StatusConnected {
		t.Errorf("Answerer should be connected, got %v", answererFinalStatus)
		
		// Debug: check what was written to answerer TTY
		answererOutput := answererTTY.GetWrittenString()
		t.Logf("Answerer TTY output: %q", answererOutput)
	}
}

// Test bidirectional data transfer between connected modems
func TestModem_BidirectionalDataTransfer(t *testing.T) {
	// Create TTYs for both modems
	callerTTY := NewMockReadWriteCloser([]byte{})
	answererTTY := NewMockReadWriteCloser([]byte{})
	
	// Create mock network connection between modems
	callerConn, answererConn := NewMockConnection()
	
	// Create outgoing call handler for caller modem
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		return callerConn, nil
	}
	
	// Create modems
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
		AnswerChar:   "C",
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	answererConfig := &ModemConfig{
		Id:         "answerer",
		TTY:        answererTTY,
		AnswerChar: "C",
	}
	
	answerer, err := NewModem(answererConfig)
	if err != nil {
		t.Fatalf("Failed to create answerer modem: %v", err)
	}
	defer answerer.CloseSync()
	
	// Wait for initialization
	time.Sleep(20 * time.Millisecond)
	
	// Establish connection
	err = answerer.IncomingCallSync(answererConn)
	if err != nil {
		t.Fatalf("Failed to set up incoming call: %v", err)
	}
	
	// Dial and answer
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	time.Sleep(30 * time.Millisecond)
	
	answererTTY.WriteInput([]byte("ATA\r"))
	time.Sleep(100 * time.Millisecond)
	
	// Verify connection established
	if caller.StatusSync() != StatusConnected || answerer.StatusSync() != StatusConnected {
		t.Fatalf("Connection not established. Caller: %v, Answerer: %v", 
			caller.StatusSync(), answerer.StatusSync())
	}
	
	// Clear TTY buffers
	callerTTY.ClearWrites()
	answererTTY.ClearWrites()
	
	// Test data transfer: Caller to Answerer
	testMessage1 := "Hello from caller!"
	callerTTY.WriteInput([]byte(testMessage1))
	
	// Wait for data to propagate
	time.Sleep(50 * time.Millisecond)
	
	// Check if answerer received the data
	answererReceived := answererTTY.GetWrittenString()
	if !strings.Contains(answererReceived, testMessage1) {
		t.Errorf("Answerer should receive %q, got %q", testMessage1, answererReceived)
	}
	
	// Clear buffers for reverse test
	callerTTY.ClearWrites()
	answererTTY.ClearWrites()
	
	// Test data transfer: Answerer to Caller
	testMessage2 := "Hello from answerer!"
	answererTTY.WriteInput([]byte(testMessage2))
	
	// Wait for data to propagate
	time.Sleep(50 * time.Millisecond)
	
	// Check if caller received the data
	callerReceived := callerTTY.GetWrittenString()
	if !strings.Contains(callerReceived, testMessage2) {
		t.Errorf("Caller should receive %q, got %q", testMessage2, callerReceived)
	}
	
	// Test simultaneous bidirectional transfer
	callerTTY.ClearWrites()
	answererTTY.ClearWrites()
	
	// Send data simultaneously
	simMessage1 := "Simultaneous1"
	simMessage2 := "Simultaneous2"
	
	callerTTY.WriteInput([]byte(simMessage1))
	answererTTY.WriteInput([]byte(simMessage2))
	
	// Wait for propagation
	time.Sleep(50 * time.Millisecond)
	
	// Verify both messages were received
	callerReceived = callerTTY.GetWrittenString()
	answererReceived = answererTTY.GetWrittenString()
	
	if !strings.Contains(callerReceived, simMessage2) {
		t.Errorf("Caller should receive %q, got %q", simMessage2, callerReceived)
	}
	
	if !strings.Contains(answererReceived, simMessage1) {
		t.Errorf("Answerer should receive %q, got %q", simMessage1, answererReceived)
	}
}

// Test escape sequence and return to command mode during connection
func TestModem_EscapeSequenceDuringConnection(t *testing.T) {
	// Create TTYs and connection
	callerTTY := NewMockReadWriteCloser([]byte{})
	answererTTY := NewMockReadWriteCloser([]byte{})
	callerConn, answererConn := NewMockConnection()
	
	// Create modems with guard time
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		return callerConn, nil
	}
	
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
		GuardTime:    2, // Short guard time for testing (100ms)
		AnswerChar:   "C",
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	answererConfig := &ModemConfig{
		Id:         "answerer",
		TTY:        answererTTY,
		GuardTime:  2,
		AnswerChar: "C",
	}
	
	answerer, err := NewModem(answererConfig)
	if err != nil {
		t.Fatalf("Failed to create answerer modem: %v", err)
	}
	defer answerer.CloseSync()
	
	// Wait for initialization
	time.Sleep(20 * time.Millisecond)
	
	// Establish connection
	answerer.IncomingCallSync(answererConn)
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	time.Sleep(30 * time.Millisecond)
	answererTTY.WriteInput([]byte("ATA\r"))
	time.Sleep(100 * time.Millisecond)
	
	// Verify connected
	if caller.StatusSync() != StatusConnected {
		t.Fatalf("Caller not connected: %v", caller.StatusSync())
	}
	
	// Clear buffers
	callerTTY.ClearWrites()
	
	// Send escape sequence (3 plus signs with proper timing)
	callerTTY.WriteInput([]byte("+++"))
	
	// Wait for escape sequence to be processed
	time.Sleep(200 * time.Millisecond)
	
	// Verify caller is now in command mode
	if caller.StatusSync() != StatusConnectedCmd {
		t.Errorf("Caller should be in command mode after escape, got %v", caller.StatusSync())
	}
	
	// Verify OK response for escape sequence
	response := callerTTY.GetWrittenString()
	if !strings.Contains(response, "OK") {
		t.Errorf("Expected OK response to escape sequence, got %q", response)
	}
	
	// Test AT command in command mode
	callerTTY.ClearWrites()
	callerTTY.WriteInput([]byte("ATH\r")) // Hangup command
	
	time.Sleep(50 * time.Millisecond)
	
	// Verify hangup occurred
	if caller.StatusSync() != StatusIdle {
		t.Errorf("Caller should be idle after hangup, got %v", caller.StatusSync())
	}
	
	// Verify answerer also disconnected
	time.Sleep(50 * time.Millisecond)
	if answerer.StatusSync() != StatusIdle {
		t.Errorf("Answerer should be idle after remote hangup, got %v", answerer.StatusSync())
	}
}

// Test connection failure scenarios
func TestModem_ConnectionFailure(t *testing.T) {
	callerTTY := NewMockReadWriteCloser([]byte{})
	
	// Create outgoing call handler that fails
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		return nil, ErrNoCarrier // Simulate connection failure
	}
	
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	// Wait for initialization
	time.Sleep(20 * time.Millisecond)
	
	// Attempt to dial
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	
	// Wait for dial attempt to complete
	time.Sleep(100 * time.Millisecond)
	
	// Verify caller returned to idle after failure
	if caller.StatusSync() != StatusIdle {
		t.Errorf("Caller should return to idle after failed dial, got %v", caller.StatusSync())
	}
	
	// Verify NO CARRIER response (or similar error indication)
	response := callerTTY.GetWrittenString()
	// The exact response depends on implementation, but should indicate failure
	if strings.Contains(response, "CONNECT") {
		t.Errorf("Should not receive CONNECT on failed call, got %q", response)
	}
}

// Test metrics during inter-modem communication
func TestModem_CommunicationMetrics(t *testing.T) {
	// Create TTYs and connection
	callerTTY := NewMockReadWriteCloser([]byte{})
	answererTTY := NewMockReadWriteCloser([]byte{})
	callerConn, answererConn := NewMockConnection()
	
	// Create modems
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		return callerConn, nil
	}
	
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
		AnswerChar:   "C",
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	answererConfig := &ModemConfig{
		Id:         "answerer",
		TTY:        answererTTY,
		AnswerChar: "C",
	}
	
	answerer, err := NewModem(answererConfig)
	if err != nil {
		t.Fatalf("Failed to create answerer modem: %v", err)
	}
	defer answerer.CloseSync()
	
	// Wait for initialization
	time.Sleep(20 * time.Millisecond)
	
	// Get initial metrics
	callerMetrics := caller.MetricsSync()
	answererMetrics := answerer.MetricsSync()
	
	initialCallerConns := callerMetrics.NumConns
	initialAnswererConns := answererMetrics.NumConns
	
	// Establish connection
	answerer.IncomingCallSync(answererConn)
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	time.Sleep(30 * time.Millisecond)
	answererTTY.WriteInput([]byte("ATA\r"))
	time.Sleep(100 * time.Millisecond)
	
	// Get metrics after connection
	callerMetrics = caller.MetricsSync()
	answererMetrics = answerer.MetricsSync()
	
	// Verify connection counts increased
	if callerMetrics.NumConns != initialCallerConns+1 {
		t.Errorf("Caller connection count should increase by 1, got %d -> %d", 
			initialCallerConns, callerMetrics.NumConns)
	}
	
	if answererMetrics.NumConns != initialAnswererConns+1 {
		t.Errorf("Answerer connection count should increase by 1, got %d -> %d", 
			initialAnswererConns, answererMetrics.NumConns)
	}
	
	// Verify outgoing vs incoming counters
	if callerMetrics.NumOutConns == 0 {
		t.Error("Caller should have outgoing connection count > 0")
	}
	
	if answererMetrics.NumInConns == 0 {
		t.Error("Answerer should have incoming connection count > 0")
	}
	
	// Test data transfer and byte counting
	testData := "Test data for metrics!"
	callerTTY.WriteInput([]byte(testData))
	time.Sleep(50 * time.Millisecond)
	
	// Get updated metrics
	callerMetrics = caller.MetricsSync()
	answererMetrics = answerer.MetricsSync()
	
	// Verify byte counters
	if callerMetrics.ConnTxBytes == 0 {
		t.Error("Caller should have transmitted bytes to connection")
	}
	
	if answererMetrics.ConnRxBytes == 0 {
		t.Error("Answerer should have received bytes from connection")
	}
	
	// Verify timestamps are updated
	if callerMetrics.LastConnTime.IsZero() {
		t.Error("Caller last connection time should be set")
	}
	
	if answererMetrics.LastConnTime.IsZero() {
		t.Error("Answerer last connection time should be set")
	}
}

// Test return to online mode from command mode
func TestModem_ReturnToOnlineMode(t *testing.T) {
	// Create TTYs and connection
	callerTTY := NewMockReadWriteCloser([]byte{})
	answererTTY := NewMockReadWriteCloser([]byte{})
	callerConn, answererConn := NewMockConnection()
	
	// Create modems
	outgoingCall := func(m *Modem, number string) (io.ReadWriteCloser, error) {
		return callerConn, nil
	}
	
	callerConfig := &ModemConfig{
		Id:           "caller",
		TTY:          callerTTY,
		OutgoingCall: outgoingCall,
		GuardTime:    2,
		AnswerChar:   "C",
	}
	
	caller, err := NewModem(callerConfig)
	if err != nil {
		t.Fatalf("Failed to create caller modem: %v", err)
	}
	defer caller.CloseSync()
	
	answererConfig := &ModemConfig{
		Id:         "answerer",
		TTY:        answererTTY,
		AnswerChar: "C",
	}
	
	answerer, err := NewModem(answererConfig)
	if err != nil {
		t.Fatalf("Failed to create answerer modem: %v", err)
	}
	defer answerer.CloseSync()
	
	// Wait for initialization
	time.Sleep(20 * time.Millisecond)
	
	// Establish connection
	answerer.IncomingCallSync(answererConn)
	callerTTY.WriteInput([]byte("ATDT12345\r"))
	time.Sleep(30 * time.Millisecond)
	answererTTY.WriteInput([]byte("ATA\r"))
	time.Sleep(100 * time.Millisecond)
	
	// Enter command mode with escape sequence
	callerTTY.ClearWrites()
	callerTTY.WriteInput([]byte("+++"))
	time.Sleep(200 * time.Millisecond)
	
	// Verify in command mode
	if caller.StatusSync() != StatusConnectedCmd {
		t.Fatalf("Caller should be in command mode, got %v", caller.StatusSync())
	}
	
	// Return to online mode with ATO command
	callerTTY.ClearWrites()
	callerTTY.WriteInput([]byte("ATO\r"))
	time.Sleep(50 * time.Millisecond)
	
	// Verify back in connected mode
	if caller.StatusSync() != StatusConnected {
		t.Errorf("Caller should be back in connected mode, got %v", caller.StatusSync())
	}
	
	// Verify data transfer still works
	answererTTY.ClearWrites()
	callerTTY.WriteInput([]byte("Back online!"))
	time.Sleep(50 * time.Millisecond)
	
	answererReceived := answererTTY.GetWrittenString()
	if !strings.Contains(answererReceived, "Back online!") {
		t.Errorf("Data transfer should work after returning online, got %q", answererReceived)
	}
}