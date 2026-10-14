package main

import (
	"encoding/json"
	"testing"
)

func TestCryptoPriceRequiresPositiveInteger(t *testing.T) {
	cfg := Config{TONPrice7: "150000000", TONPrice30: "bad", TONPrice90: "0", USDTPrice7: "1500000"}
	if got := cryptoPrice("TON", 7, cfg); got != "150000000" {
		t.Fatalf("got %q", got)
	}
	for _, days := range []int{30, 90, 10} {
		if got := cryptoPrice("TON", days, cfg); got != "" {
			t.Fatalf("days %d: got %q", days, got)
		}
	}
	if got := cryptoPrice("USDT", 7, cfg); got != "1500000" {
		t.Fatalf("got %q", got)
	}
}

func TestParseTONTransfer(t *testing.T) {
	fixture := json.RawMessage(`{"event_id":"evt-123","actions":[{"type":"TonTransfer","status":"ok","TonTransfer":{"sender":{"address":"0:sender"},"recipient":{"address":"0:receiver"},"amount":"150000000","comment":"vpn-order-1"}}]}`)
	memo, amount, recipient, _, tx, status := parseTransfer(fixture, "TON")
	if memo != "vpn-order-1" || amount != "150000000" || recipient != "0:receiver" || tx != "evt-123" || status != "ok" {
		t.Fatalf("unexpected transfer: %q %q %q %q %q", memo, amount, recipient, tx, status)
	}
}

func TestParseUSDTTransferRequiresJettonAction(t *testing.T) {
	fixture := json.RawMessage(`{"event_id":"evt-456","actions":[{"type":"JettonTransfer","status":"ok","JettonTransfer":{"sender":{"address":"0:sender"},"recipient":{"address":"0:receiver"},"amount":"1500000","comment":"vpn-order-2","jetton":{"address":"0:usdtmaster","decimals":6}}}]}`)
	memo, amount, recipient, master, tx, status := parseTransfer(fixture, "USDT")
	if memo != "vpn-order-2" || amount != "1500000" || recipient != "0:receiver" || master != "0:usdtmaster" || tx != "evt-456" || status != "ok" {
		t.Fatalf("unexpected transfer: %q %q %q %q %q %q", memo, amount, recipient, master, tx, status)
	}
	if m, _, _, _, _, _ := parseTransfer(fixture, "TON"); m != "" {
		t.Fatalf("USDT event accepted as TON: %q", m)
	}
}

func TestFormatCryptoAmount(t *testing.T) {
	cases := []struct{ asset, smallest, want string }{
		{"TON", "150000000", "0.15"},
		{"TON", "1000000000", "1"},
		{"USDT", "1500000", "1.5"},
		{"USDT", "1000000", "1"},
	}
	for _, tc := range cases {
		if got := formatCryptoAmount(tc.asset, tc.smallest); got != tc.want {
			t.Errorf("formatCryptoAmount(%s,%s)=%s want %s", tc.asset, tc.smallest, got, tc.want)
		}
	}
}
