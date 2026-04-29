package slackflag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func computeSig(t *testing.T, secret, ts string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := "test-secret"
	ts := "1714000000"
	body := []byte("token=abc&team_id=T1")
	sig := computeSig(t, secret, ts, body)
	now := time.Unix(1714000000, 0)

	if err := verifySignature(secret, ts, body, sig, now, 5*time.Minute); err != nil {
		t.Fatalf("good sig should pass: %v", err)
	}

	if err := verifySignature(secret, ts, body, "v0=deadbeef", now, 5*time.Minute); err == nil {
		t.Fatal("bad sig must fail")
	}

	tampered := []byte("token=abc&team_id=T2")
	if err := verifySignature(secret, ts, tampered, sig, now, 5*time.Minute); err == nil {
		t.Fatal("tampered body must fail")
	}

	later := now.Add(10 * time.Minute)
	if err := verifySignature(secret, ts, body, sig, later, 5*time.Minute); err == nil {
		t.Fatal("expired ts must fail")
	}

	if err := verifySignature(secret, "abc", body, sig, now, 5*time.Minute); err == nil {
		t.Fatal("non-numeric ts must fail")
	}

	if err := verifySignature("", ts, body, sig, now, 5*time.Minute); err == nil {
		t.Fatal("empty secret must fail")
	}
}
