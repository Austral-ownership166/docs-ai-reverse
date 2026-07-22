package inkeep

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSolveChallenge(t *testing.T) {
	salt := "abc"
	n := 42
	sum := sha256.Sum256([]byte(salt + "42"))
	challenge := hex.EncodeToString(sum[:])
	sol, err := SolveChallenge(challengeData{
		Challenge: challenge,
		Salt:      salt,
		MaxNumber: 100,
		Algorithm: "SHA-256",
		Signature: "sig",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sol == "" {
		t.Fatal("empty solution")
	}
	_ = n
}
