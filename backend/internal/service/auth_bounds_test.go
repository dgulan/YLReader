package service

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestDummyPasswordHashUsesDefaultVerificationCost(t *testing.T) {
	parts := strings.Split(dummyHash, "$")
	if len(parts) != 6 || parts[2] != "v=19" || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonIterations, argonParallelism) {
		t.Fatal("dummy PHC must retain the real default cost")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != argonSaltLength {
		t.Fatal("invalid dummy salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) != argonKeyLength {
		t.Fatal("invalid dummy key")
	}
	if ok, err := VerifyPassword(dummyHash, "synthetic-wrong-password"); err != nil || ok {
		t.Fatal("dummy must execute verification without authenticating a password")
	}
}

func TestPasswordHashResourceBounds(t *testing.T) {
	for _, phc := range []string{
		"$argon2id$v=19$m=4294967295,t=3,p=2$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=0,p=2$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=0$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=18$m=65536,t=3,p=2$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=2junk$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=2$AAAA$AAAA",
	} {
		if ok, err := VerifyPassword(phc, "test"); err == nil || ok {
			t.Fatal("unsafe PHC accepted")
		}
	}
}
