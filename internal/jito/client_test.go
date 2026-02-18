package jito

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	c := NewClient("", 0)
	if c.blockEngineURL != DefaultBlockEngineURL {
		t.Errorf("expected default URL %q, got %q", DefaultBlockEngineURL, c.blockEngineURL)
	}
	if c.tipLamports != DefaultTipLamports {
		t.Errorf("expected default tip %d, got %d", DefaultTipLamports, c.tipLamports)
	}

	c2 := NewClient("https://custom.jito.wtf", 50000)
	if c2.blockEngineURL != "https://custom.jito.wtf" {
		t.Errorf("expected custom URL, got %q", c2.blockEngineURL)
	}
	if c2.tipLamports != 50000 {
		t.Errorf("expected custom tip 50000, got %d", c2.tipLamports)
	}
}

func TestTipAccounts(t *testing.T) {
	c := NewClient("", 0)
	accounts := c.TipAccounts()
	if len(accounts) != 8 {
		t.Errorf("expected 8 tip accounts, got %d", len(accounts))
	}

	// Verify tip accounts are distinct.
	seen := make(map[string]bool)
	for _, a := range accounts {
		if a == "" {
			t.Error("empty tip account")
		}
		if seen[a] {
			t.Errorf("duplicate tip account: %s", a)
		}
		seen[a] = true
	}

	// Verify returned slice is a copy (modifying it doesn't affect the original).
	accounts[0] = "modified"
	fresh := c.TipAccounts()
	if fresh[0] == "modified" {
		t.Error("TipAccounts should return a copy, not the original slice")
	}
}

func TestSendBundle_BadURL(t *testing.T) {
	c := NewClient("http://localhost:1", 10000)
	_, err := c.SendBundle(context.Background(), []string{"dHgx", "dHgy"})
	if err == nil {
		t.Fatal("expected error for bad URL, got nil")
	}
}

func TestSendBundle_MockServer(t *testing.T) {
	var receivedReq jsonRPCRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/bundles" {
			t.Errorf("expected path /api/v1/bundles, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "bundle-id-123",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10000)
	bundleID, err := c.SendBundle(context.Background(), []string{"tx1_base64", "tx2_base64"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if bundleID != "bundle-id-123" {
		t.Errorf("expected bundle ID 'bundle-id-123', got %q", bundleID)
	}

	// Verify JSON-RPC format.
	if receivedReq.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc '2.0', got %q", receivedReq.JSONRPC)
	}
	if receivedReq.Method != "sendBundle" {
		t.Errorf("expected method 'sendBundle', got %q", receivedReq.Method)
	}
	if receivedReq.ID != 1 {
		t.Errorf("expected id 1, got %d", receivedReq.ID)
	}

	// Params should be [[tx1, tx2]].
	params, ok := receivedReq.Params.([]interface{})
	if !ok || len(params) != 1 {
		t.Fatalf("expected params to be array of length 1, got %v", receivedReq.Params)
	}
	txns, ok := params[0].([]interface{})
	if !ok || len(txns) != 2 {
		t.Fatalf("expected inner array of 2 txns, got %v", params[0])
	}
	if txns[0] != "tx1_base64" || txns[1] != "tx2_base64" {
		t.Errorf("unexpected txn values: %v", txns)
	}
}

func TestSendBundle_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]interface{}{
				"code":    -32000,
				"message": "bundle rejected",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10000)
	_, err := c.SendBundle(context.Background(), []string{"tx1"})
	if err == nil {
		t.Fatal("expected RPC error, got nil")
	}
}

func TestGetBundleStatus_MockServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method != "getBundleStatuses" {
			t.Errorf("expected method getBundleStatuses, got %q", req.Method)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"value": []map[string]interface{}{
					{
						"bundle_id":           "bundle-123",
						"confirmation_status": "finalized",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10000)
	status, err := c.GetBundleStatus(context.Background(), "bundle-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "landed" {
		t.Errorf("expected status 'landed', got %q", status)
	}
}

func TestGetBundleStatus_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"value": []interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10000)
	status, err := c.GetBundleStatus(context.Background(), "bundle-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "pending" {
		t.Errorf("expected status 'pending', got %q", status)
	}
}
