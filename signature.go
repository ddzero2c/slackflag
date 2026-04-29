package slackflag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

func verifySignature(secret, ts string, body []byte, sig string, now time.Time, skew time.Duration) error {
	if secret == "" {
		return fmt.Errorf("empty signing secret")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp %q: %w", ts, err)
	}
	delta := now.Sub(time.Unix(tsInt, 0))
	if delta > skew || delta < -skew {
		return fmt.Errorf("timestamp outside skew: %v", delta)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
