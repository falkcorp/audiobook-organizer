// file: internal/realtime/events_test.go
// version: 1.3.1
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-09-02

package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
)

func TestNewClient(t *testing.T) {
	clientID := "test-client-123"
	client := NewClient(clientID)

	if client.ID != clientID {
		t.Errorf("Expected client ID %s, got %s", clientID, client.ID)
	}
	if client.Channel == nil {
		t.Error("Expected non-nil channel")
	}
	if client.Operations == nil {
		t.Error("Expected non-nil operations map")
	}
	if len(client.Operations) != 0 {
		t.Error("Expected empty operations map initially")
	}
}

func TestClient_Subscribe(t *testing.T) {
	client := NewClient("test-client")
	operationID := "op-123"

	client.Subscribe(operationID)

	if !client.IsSubscribed(operationID) {
		t.Errorf("Client should be subscribed to operation %s", operationID)
	}
}

func TestClient_Unsubscribe(t *testing.T) {
	client := NewClient("test-client")
	operationID := "op-123"

	client.Subscribe(operationID)
	client.Unsubscribe(operationID)

	if client.IsSubscribed(operationID) {
		t.Errorf("Client should not be subscribed to operation %s after unsubscribe", operationID)
	}
}

func TestClient_IsSubscribed(t *testing.T) {
	client := NewClient("test-client")

	// Test not subscribed initially
	if client.IsSubscribed("op-1") {
		t.Error("Client should not be subscribed initially")
	}

	// Subscribe and test
	client.Subscribe("op-1")
	if !client.IsSubscribed("op-1") {
		t.Error("Client should be subscribed after Subscribe()")
	}

	// Test different operation
	if client.IsSubscribed("op-2") {
		t.Error("Client should not be subscribed to different operation")
	}
}

func TestNewEventHub(t *testing.T) {
	hub := NewEventHub()

	if hub.clients == nil {
		t.Error("Expected non-nil clients map")
	}
	if len(hub.clients) != 0 {
		t.Error("Expected empty clients map initially")
	}
}

func TestEventHub_RegisterClient(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")

	hub.RegisterClient(client)

	if hub.GetClientCount() != 1 {
		t.Errorf("Expected 1 client, got %d", hub.GetClientCount())
	}
}

func TestEventHub_UnregisterClient(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")

	hub.RegisterClient(client)
	hub.UnregisterClient(client.ID)

	if hub.GetClientCount() != 0 {
		t.Errorf("Expected 0 clients, got %d", hub.GetClientCount())
	}
}

func TestEventHub_GetClientCount(t *testing.T) {
	hub := NewEventHub()

	if hub.GetClientCount() != 0 {
		t.Error("Expected 0 clients initially")
	}

	// Add clients
	for i := range 5 {
		client := NewClient(fmt.Sprintf("client-%d", i))
		hub.RegisterClient(client)
	}

	if hub.GetClientCount() != 5 {
		t.Errorf("Expected 5 clients, got %d", hub.GetClientCount())
	}
}

func TestEventHub_Broadcast_SystemWideEvent(t *testing.T) {
	hub := NewEventHub()

	// Register clients
	client1 := NewClient("client-1")
	client2 := NewClient("client-2")
	hub.RegisterClient(client1)
	hub.RegisterClient(client2)

	// Create system-wide event (no ID)
	event := &Event{
		Type:      EventSystemStatus,
		ID:        "",
		Timestamp: time.Now(),
		Data: map[string]any{
			"status": "running",
		},
	}

	// Broadcast
	hub.Broadcast(event)

	// Both clients should receive the event
	select {
	case receivedEvent := <-client1.Channel:
		if receivedEvent.Type != EventSystemStatus {
			t.Error("Client 1 received wrong event type")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Client 1 did not receive event")
	}

	select {
	case receivedEvent := <-client2.Channel:
		if receivedEvent.Type != EventSystemStatus {
			t.Error("Client 2 received wrong event type")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Client 2 did not receive event")
	}
}

func TestEventHub_Broadcast_OperationSpecificEvent(t *testing.T) {
	hub := NewEventHub()

	// Register clients with different subscriptions
	client1 := NewClient("client-1")
	client1.Subscribe("op-1")

	client2 := NewClient("client-2")
	client2.Subscribe("op-2")

	hub.RegisterClient(client1)
	hub.RegisterClient(client2)

	// Broadcast event for op-1
	event := &Event{
		Type:      EventOperationProgress,
		ID:        "op-1",
		Timestamp: time.Now(),
		Data: map[string]any{
			"progress": 50,
		},
	}

	hub.Broadcast(event)

	// Client1 should receive, client2 should not
	select {
	case <-client1.Channel:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Client 1 did not receive operation-specific event")
	}

	select {
	case <-client2.Channel:
		t.Error("Client 2 should not receive event for different operation")
	case <-time.After(50 * time.Millisecond):
		// Expected - no event received
	}
}

func TestEventHub_SendOperationProgress(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	client.Subscribe("op-123")
	hub.RegisterClient(client)

	hub.SendOperationProgress("op-123", 50, 100, "Processing...")

	select {
	case event := <-client.Channel:
		if event.Type != EventOperationProgress {
			t.Error("Received wrong event type")
		}
		if event.ID != "op-123" {
			t.Error("Received wrong operation ID")
		}
		if event.Data["current"] != 50 {
			t.Error("Wrong current value")
		}
		if event.Data["total"] != 100 {
			t.Error("Wrong total value")
		}
		if event.Data["percentage"] != 50 {
			t.Error("Wrong percentage calculation")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive operation progress event")
	}
}

func TestEventHub_SendOperationStatus(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	client.Subscribe("op-123")
	hub.RegisterClient(client)

	details := map[string]any{
		"files_processed": 42,
	}
	hub.SendOperationStatus("op-123", "completed", details)

	select {
	case event := <-client.Channel:
		if event.Type != EventOperationStatus {
			t.Error("Received wrong event type")
		}
		if event.Data["status"] != "completed" {
			t.Error("Wrong status")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive operation status event")
	}
}

func TestEventHub_SendOperationLog(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	client.Subscribe("op-123")
	hub.RegisterClient(client)

	message := "Processing file"
	detailsStr := "file.mp3"
	hub.SendOperationLog("op-123", "info", message, &detailsStr)

	select {
	case event := <-client.Channel:
		if event.Type != EventOperationLog {
			t.Error("Received wrong event type")
		}
		if event.Data["level"] != "info" {
			t.Error("Wrong log level")
		}
		if event.Data["message"] != message {
			t.Error("Wrong message")
		}
		if event.Data["details"] != detailsStr {
			t.Error("Wrong details")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive operation log event")
	}
}

func TestEventHub_SendSystemStatus(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	hub.RegisterClient(client)

	data := map[string]any{
		"uptime": "10m",
	}
	hub.SendSystemStatus(data)

	select {
	case event := <-client.Channel:
		if event.Type != EventSystemStatus {
			t.Error("Received wrong event type")
		}
		if event.ID != "" {
			t.Error("System status event should have empty ID")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive system status event")
	}
}

func TestCalculatePercentage(t *testing.T) {
	tests := []struct {
		current  int
		total    int
		expected int
	}{
		{0, 100, 0},
		{50, 100, 50},
		{100, 100, 100},
		{150, 100, 100}, // Should cap at 100
		{0, 0, 0},       // Edge case: total is 0
		{10, 0, 0},      // Edge case: total is 0
		{-5, 100, -5},   // Negative current (edge case)
	}

	for _, tt := range tests {
		result := calculatePercentage(tt.current, tt.total)
		if result != tt.expected {
			t.Errorf("calculatePercentage(%d, %d) = %d, want %d",
				tt.current, tt.total, result, tt.expected)
		}
	}
}

func TestInitializeEventHub(t *testing.T) {
	// Save old global hub
	oldHub := GetGlobalHub()
	defer func() { SetGlobalHub(oldHub) }()

	// Reset and initialize
	SetGlobalHub(nil)
	InitializeEventHub()

	if GetGlobalHub() == nil {
		t.Error("Expected GlobalHub to be initialized")
	}

	// Test idempotency
	prevHub := GetGlobalHub()
	InitializeEventHub()
	if GetGlobalHub() != prevHub {
		t.Error("InitializeEventHub should be idempotent")
	}
}

// TestEventHub_Broadcast_ChannelFull tests the scenario where client channel is full
func TestEventHub_Broadcast_ChannelFull(t *testing.T) {
	hub := NewEventHub()

	// Create client with buffer of 0 to make it immediately full
	client := &Client{
		ID:         "client-1",
		Channel:    make(chan *Event), // No buffer
		Operations: make(map[string]bool),
	}
	hub.RegisterClient(client)

	// Send event in background since channel is full
	go func() {
		event := &Event{
			Type:      EventSystemStatus,
			ID:        "",
			Timestamp: time.Now(),
			Data: map[string]any{
				"status": "test",
			},
		}
		hub.Broadcast(event)
	}()

	// Give some time for broadcast to attempt
	time.Sleep(50 * time.Millisecond)

	// The event should be dropped due to full channel (non-blocking select)
	// This tests the default case in Broadcast
	select {
	case <-client.Channel:
		// If we receive something, that's ok - timing dependent
	default:
		// Expected if channel was full
	}

	hub.UnregisterClient(client.ID)
}

// TestEventHub_UnregisterClient_NonExistent tests unregistering a non-existent client
func TestEventHub_UnregisterClient_NonExistent(t *testing.T) {
	hub := NewEventHub()

	// Unregister client that doesn't exist - should not panic
	hub.UnregisterClient("non-existent-client")

	if hub.GetClientCount() != 0 {
		t.Error("Expected 0 clients")
	}
}

// TestEventHub_Broadcast_NoClients tests broadcasting with no clients
func TestEventHub_Broadcast_NoClients(t *testing.T) {
	hub := NewEventHub()

	event := &Event{
		Type:      EventSystemStatus,
		ID:        "",
		Timestamp: time.Now(),
		Data: map[string]any{
			"status": "test",
		},
	}

	// Should not panic with no clients
	hub.Broadcast(event)
}

// TestEventHub_Broadcast_ClientWithNoSubscriptions tests client with no subscriptions receives all events
func TestEventHub_Broadcast_ClientWithNoSubscriptions(t *testing.T) {
	hub := NewEventHub()

	// Client with no subscriptions
	client := NewClient("client-1")
	hub.RegisterClient(client)

	// Send operation-specific event
	event := &Event{
		Type:      EventOperationProgress,
		ID:        "op-123",
		Timestamp: time.Now(),
		Data: map[string]any{
			"progress": 50,
		},
	}

	hub.Broadcast(event)

	// Client should receive event even though it has no subscriptions
	select {
	case <-client.Channel:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Client with no subscriptions should receive all events")
	}
}

// TestEventHub_SendOperationLog_WithoutDetails tests sending operation log without details
func TestEventHub_SendOperationLog_WithoutDetails(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	client.Subscribe("op-123")
	hub.RegisterClient(client)

	hub.SendOperationLog("op-123", "info", "Test message", nil)

	select {
	case event := <-client.Channel:
		if event.Type != EventOperationLog {
			t.Error("Received wrong event type")
		}
		if _, hasDetails := event.Data["details"]; hasDetails {
			t.Error("Should not have details field when nil")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive operation log event")
	}
}

// TestClient_ConcurrentSubscribe tests concurrent subscribe/unsubscribe operations
func TestClient_ConcurrentSubscribe(t *testing.T) {
	client := NewClient("test-client")

	// Concurrent subscribe operations
	done := make(chan bool)
	for i := range 10 {
		go func(id int) {
			opID := fmt.Sprintf("op-%d", id)
			client.Subscribe(opID)
			if !client.IsSubscribed(opID) {
				t.Errorf("Client should be subscribed to %s", opID)
			}
			client.Unsubscribe(opID)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}
}

// TestHandleSSE_BasicConnection tests the basic SSE connection
func TestHandleSSE_BasicConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewEventHub()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/events", nil)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)

	// Run HandleSSE in goroutine
	done := make(chan struct{})
	go func() {
		hub.HandleSSE(c)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for SSE handler to finish")
	}

	// Check headers after handler completes to avoid races
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Error("Expected Content-Type: text/event-stream")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-cache") {
		t.Error("Expected Cache-Control to include no-cache")
	}
	if w.Header().Get("Connection") != "keep-alive" {
		t.Error("Expected Connection: keep-alive")
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("Expected Access-Control-Allow-Origin: *")
	}

	// Check that initial event was sent
	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Error("Expected SSE data format")
	}
	if !strings.Contains(body, "connection.established") {
		t.Error("Expected connection.established event")
	}
}

// TestHandleSSE_WithOperationSubscription tests SSE with operation query parameter
func TestHandleSSE_WithOperationSubscription(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewEventHub()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/events?operation=op-123", nil)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)

	// Run HandleSSE in goroutine
	done := make(chan bool)
	go func() {
		hub.HandleSSE(c)
		done <- true
	}()

	// Give time for client to register
	time.Sleep(50 * time.Millisecond)

	// Verify client was subscribed to operation
	if hub.GetClientCount() != 1 {
		t.Error("Expected 1 client to be registered")
	}

	<-done

	// After context done, client should be unregistered
	time.Sleep(50 * time.Millisecond)
	if hub.GetClientCount() != 0 {
		t.Error("Expected client to be unregistered after disconnect")
	}
}

// TestHandleSSE_EventDelivery tests that events are properly delivered
func TestHandleSSE_EventDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewEventHub()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/events", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)

	// Run HandleSSE in goroutine
	done := make(chan bool)
	go func() {
		hub.HandleSSE(c)
		done <- true
	}()

	// Wait for connection to establish
	time.Sleep(50 * time.Millisecond)

	// Send an event
	hub.SendSystemStatus(map[string]any{
		"status": "test_event",
	})

	// Wait for event to be sent
	time.Sleep(50 * time.Millisecond)

	<-done

	// Check response body contains the event
	body := w.Body.String()
	if !strings.Contains(body, "test_event") {
		t.Error("Expected event to be in response body")
	}
	if !strings.Contains(body, "system.status") {
		t.Error("Expected system.status event type")
	}
}

// TestHandleSSE_Heartbeat tests heartbeat functionality.
//
// Runs inside a testing/synctest bubble. HandleSSE's 25s heartbeat ticker, the
// request context's timeout and this test's own wait are all created inside the
// bubble, so the fake clock advances them the instant every bubbled goroutine is
// durably blocked. The wall-clock cost drops from ~26s to ~0.
//
// The bubble is sound here because nothing in the exercised path touches the
// outside world: the hub is in-memory, the ResponseWriter is an
// httptest.NewRecorder backed by a bytes.Buffer, and there is no listener,
// socket, subprocess or signal handler. Contrast TestServerStartGracefulShutdown
// in package server, which cannot be bubbled for exactly those reasons.
//
// The former testing.Short() skip is gone deliberately: it existed only because
// the test cost 26 wall-clock seconds. It now runs in short mode too, so `make
// ci` covers the heartbeat path for the first time.
//
// SEMANTIC NOTE: time.Now() inside the bubble reports midnight UTC 2000-01-01,
// so Event.Timestamp values produced here are fake. This test asserts only on
// the ": ping" comment frame, never on a timestamp, so the fake clock changes
// nothing it checks.
func TestHandleSSE_Heartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		hub := NewEventHub()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/events", nil)

		// Longer than the 25s ticker so the heartbeat is reachable.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)

		// Run HandleSSE in goroutine
		done := make(chan bool)
		go func() {
			hub.HandleSSE(c)
			done <- true
		}()

		// Advance the fake clock past the 25s heartbeat tick, then wait for the
		// handler goroutine to become durably blocked again so the ": ping"
		// write is guaranteed to have landed before we cancel.
		time.Sleep(26 * time.Second)
		synctest.Wait()

		cancel() // Cancel to stop the handler
		<-done

		// Check response body contains SSE comment heartbeat (": ping")
		body := w.Body.String()
		if !strings.Contains(body, ": ping") {
			t.Errorf("Expected SSE comment heartbeat (': ping') in response body, got: %q", body)
		}
	})
}

// TestHandleSSE_JSONMarshalError tests handling of marshal errors
func TestHandleSSE_JSONMarshalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewEventHub()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/events", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)

	// Run HandleSSE in goroutine
	done := make(chan bool)
	go func() {
		hub.HandleSSE(c)
		done <- true
	}()

	// Wait for connection
	time.Sleep(50 * time.Millisecond)

	// Send event with unmarshalable data (channel, func, etc.)
	// In practice, json.Marshal will succeed for our Event types,
	// so this is more of a defensive test
	event := &Event{
		Type:      EventSystemStatus,
		ID:        "",
		Timestamp: time.Now(),
		Data: map[string]any{
			"normal": "value",
		},
	}
	hub.Broadcast(event)

	time.Sleep(50 * time.Millisecond)
	<-done

	// Should not crash
}

// TestCalculatePercentage_NegativeTotal tests calculatePercentage with negative total
func TestCalculatePercentage_NegativeTotal(t *testing.T) {
	result := calculatePercentage(50, -10)
	if result != 0 {
		t.Errorf("Expected 0 for negative total, got %d", result)
	}
}

// TestEventHub_MultipleSubscriptions tests client subscribed to multiple operations
func TestEventHub_MultipleSubscriptions(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")

	// Subscribe to multiple operations
	client.Subscribe("op-1")
	client.Subscribe("op-2")
	client.Subscribe("op-3")

	hub.RegisterClient(client)

	// Send event for op-2
	event := &Event{
		Type:      EventOperationProgress,
		ID:        "op-2",
		Timestamp: time.Now(),
		Data:      map[string]any{"test": "data"},
	}

	hub.Broadcast(event)

	// Client should receive it
	select {
	case <-client.Channel:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Client should receive event for subscribed operation")
	}

	// Send event for non-subscribed operation
	event2 := &Event{
		Type:      EventOperationProgress,
		ID:        "op-999",
		Timestamp: time.Now(),
		Data:      map[string]any{"test": "data"},
	}

	hub.Broadcast(event2)

	// Client should not receive it
	select {
	case <-client.Channel:
		t.Error("Client should not receive event for non-subscribed operation")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}
}

// TestEventType_Constants tests that event type constants are defined
func TestEventType_Constants(t *testing.T) {
	if EventOperationProgress != "operation.progress" {
		t.Error("EventOperationProgress has wrong value")
	}
	if EventOperationStatus != "operation.status" {
		t.Error("EventOperationStatus has wrong value")
	}
	if EventOperationLog != "operation.log" {
		t.Error("EventOperationLog has wrong value")
	}
	if EventSystemStatus != "system.status" {
		t.Error("EventSystemStatus has wrong value")
	}
}

// TestEvent_JSONMarshaling tests that Event can be marshaled to JSON
func TestEvent_JSONMarshaling(t *testing.T) {
	event := &Event{
		Type:      EventOperationProgress,
		ID:        "op-123",
		Timestamp: time.Now(),
		Data: map[string]any{
			"progress": 50,
			"message":  "test",
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Errorf("Failed to marshal event: %v", err)
	}

	if len(data) == 0 {
		t.Error("Marshaled data should not be empty")
	}

	// Unmarshal and verify
	var unmarshaled Event
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Errorf("Failed to unmarshal event: %v", err)
	}

	if unmarshaled.Type != EventOperationProgress {
		t.Error("Event type mismatch after unmarshal")
	}
	if unmarshaled.ID != "op-123" {
		t.Error("Event ID mismatch after unmarshal")
	}
}

// TestEventHub_RegisterClient_MultipleClients tests registering multiple clients
func TestEventHub_RegisterClient_MultipleClients(t *testing.T) {
	hub := NewEventHub()

	clients := make([]*Client, 10)
	for i := range 10 {
		clients[i] = NewClient(fmt.Sprintf("client-%d", i))
		hub.RegisterClient(clients[i])
	}

	if hub.GetClientCount() != 10 {
		t.Errorf("Expected 10 clients, got %d", hub.GetClientCount())
	}

	// Unregister half
	for i := range 5 {
		hub.UnregisterClient(clients[i].ID)
	}

	if hub.GetClientCount() != 5 {
		t.Errorf("Expected 5 clients after unregistering half, got %d", hub.GetClientCount())
	}
}

// TestEventHub_SendOperationProgress_EdgeCases tests edge cases for progress
func TestEventHub_SendOperationProgress_EdgeCases(t *testing.T) {
	hub := NewEventHub()
	client := NewClient("client-1")
	client.Subscribe("op-123")
	hub.RegisterClient(client)

	// Test with total = 0
	hub.SendOperationProgress("op-123", 0, 0, "Starting")

	select {
	case event := <-client.Channel:
		if event.Data["percentage"] != 0 {
			t.Error("Percentage should be 0 when total is 0")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive event")
	}

	// Test with current > total
	hub.SendOperationProgress("op-123", 150, 100, "Exceeded")

	select {
	case event := <-client.Channel:
		if event.Data["percentage"] != 100 {
			t.Error("Percentage should be capped at 100")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Did not receive event")
	}
}

// TestHandleSSE_ClientDisconnect tests client disconnection handling
func TestHandleSSE_ClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewEventHub()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/events", nil)

	// Create a context that we'll cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = c.Request.WithContext(ctx)

	// Cancel immediately to simulate disconnect
	cancel()

	// Run HandleSSE - should exit quickly due to context cancellation
	done := make(chan bool, 1)
	go func() {
		hub.HandleSSE(c)
		done <- true
	}()

	// Wait for handler to complete
	select {
	case <-done:
		// Expected - handler should exit
	case <-time.After(1 * time.Second):
		t.Error("Handler should exit quickly when context is cancelled")
	}

	// Client should be unregistered
	time.Sleep(10 * time.Millisecond)
	if hub.GetClientCount() != 0 {
		t.Error("Expected client to be unregistered")
	}
}

// TestEventHub_ConcurrentBroadcast tests concurrent broadcasting
func TestEventHub_ConcurrentBroadcast(t *testing.T) {
	hub := NewEventHub()

	// Register multiple clients
	clients := make([]*Client, 5)
	for i := range 5 {
		clients[i] = NewClient(fmt.Sprintf("client-%d", i))
		hub.RegisterClient(clients[i])
	}

	// Broadcast multiple events concurrently
	done := make(chan bool)
	for i := range 10 {
		go func(id int) {
			event := &Event{
				Type:      EventSystemStatus,
				ID:        "",
				Timestamp: time.Now(),
				Data: map[string]any{
					"id": id,
				},
			}
			hub.Broadcast(event)
			done <- true
		}(i)
	}

	// Wait for all broadcasts to complete
	for range 10 {
		<-done
	}

	// Each client should have received some events
	for i, client := range clients {
		receivedCount := 0
		timeout := time.After(100 * time.Millisecond)
	drainLoop:
		for {
			select {
			case <-client.Channel:
				receivedCount++
			case <-timeout:
				break drainLoop
			}
		}
		if receivedCount == 0 {
			t.Errorf("Client %d received no events", i)
		}
	}
}

// TestClient_ChannelCapacity tests that client channel has correct capacity
func TestClient_ChannelCapacity(t *testing.T) {
	client := NewClient("test-client")

	// Channel should have capacity of 100
	// We can't directly test capacity, but we can verify it doesn't block
	// for reasonable number of events
	for i := range 50 {
		select {
		case client.Channel <- &Event{
			Type:      EventSystemStatus,
			Timestamp: time.Now(),
			Data:      map[string]any{"i": i},
		}:
			// Expected - should not block
		default:
			t.Error("Channel should not be full after 50 events")
		}
	}
}
