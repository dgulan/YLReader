package backup

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRestoredAccountInputRejectsBeforeConnection(t *testing.T) {
	prepared, _, _, clock := intentFixture(t)
	source, target := bytes.Repeat([]byte("s"), 32), bytes.Repeat([]byte("t"), 32)
	if !validBehaviorAccountInput(prepared, source, target) {
		t.Fatal("valid bounded input rejected")
	}
	for _, name := range []string{"closed", "same_key", "short_source", "short_target", "wrong_user", "nil_user"} {
		t.Run(name, func(t *testing.T) {
			copy := &preparedBehaviorInput{consent: prepared.consent, accounts: append([]behaviorCredential(nil), prepared.accounts...)}
			a, b := source, target
			switch name {
			case "closed":
				copy.Close()
			case "same_key":
				b = a
			case "short_source":
				a = []byte("s")
			case "short_target":
				b = []byte("t")
			case "wrong_user":
				copy.accounts[0].UserID = copy.consent.ID
			case "nil_user":
				copy.accounts[0].UserID = "00000000-0000-0000-0000-000000000000"
			}
			if validBehaviorAccountInput(copy, a, b) {
				t.Fatal("invalid account input accepted")
			}
			if users, e := probeRestoredAccounts(context.Background(), "SECRET", databaseIdentityPin{}, databaseIdentityPin{}, a, b, copy, clock); e != errBehaviorAccounts || users != nil {
				t.Fatal("unsafe input reached connection")
			}
		})
	}
	var g *restoreGuard
	if proof, e := g.verifyBehaviorAccounts(context.Background(), prepared, handoverProbeInputs{}, func() time.Time { return time.Now() }); e != errBehaviorAccounts || proof != nil {
		t.Fatal("nil guard accepted")
	}
}
