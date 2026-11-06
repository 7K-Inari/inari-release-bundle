package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPoll(t *testing.T) {
	fast := []time.Duration{200 * time.Millisecond, 5 * time.Millisecond}

	t.Run("succeeds after retries", func(t *testing.T) {
		calls := 0
		err := poll(fast[0], fast[1], func() error {
			calls++
			if calls < 3 {
				return errors.New("not yet")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("times out with the last error", func(t *testing.T) {
		err := poll(fast[0], fast[1], func() error { return errors.New("the real reason") })
		if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "the real reason") || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("fatalError aborts immediately", func(t *testing.T) {
		calls := 0
		err := poll(fast[0], fast[1], func() error {
			calls++
			return &fatalError{fmt.Errorf("broken membership API")}
		})
		if err == nil || !strings.Contains(err.Error(), "broken membership API") {
			t.Fatalf("err = %v", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1 (no retry on fatal)", calls)
		}
	})
}

func TestJWTClaimsAndAud(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u-1","aud":["account","inari-server"]}`))
	token := "x." + payload + ".y"
	claims, err := jwtClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != "u-1" {
		t.Fatalf("sub = %v", claims["sub"])
	}
	if !audContains(claims["aud"], "inari-server") {
		t.Fatalf("aud = %v", claims["aud"])
	}
	if audContains("other", "inari-server") || audContains(claims["aud"], "nope") {
		t.Fatal("audContains false positive")
	}
}
