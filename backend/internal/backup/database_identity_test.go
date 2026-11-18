package backup

import (
	"context"
	"errors"
	"testing"
)

func TestDatabaseIdentityPinCanonicalAndSensitive(t *testing.T) {
	p := databaseIdentityPin{"123456789", 100, 200}
	h, e := p.fingerprint()
	if e != nil || !validSHA(h) {
		t.Fatal("invalid fingerprint", e)
	}
	if h != "13b0fe19024c68d499eccaad9c08e7322e8ffe420d53ec5c204e3af2b0a7efd3" {
		t.Fatal("v1 canonical fingerprint drift")
	}
	for _, q := range []databaseIdentityPin{{"123456790", 100, 200}, {"123456789", 101, 200}, {"123456789", 100, 201}} {
		next, e := q.fingerprint()
		if e != nil || next == h {
			t.Fatal("identity component unbound")
		}
	}
	for _, q := range []databaseIdentityPin{{"", 100, 200}, {"0", 100, 200}, {"0123", 100, 200}, {"+123", 100, 200}, {"-1", 100, 200}, {"18446744073709551616", 100, 200}, {"123", 0, 200}, {"123", 100, 0}} {
		if q.valid() {
			t.Fatal("invalid pin accepted")
		}
		if _, e := q.fingerprint(); !errors.Is(e, errDatabaseIdentity) {
			t.Fatal(e)
		}
	}
}

func TestDatabaseIdentitySourceTargetSeparation(t *testing.T) {
	s := databaseIdentityPin{"123", 100, 200}
	if databasePinsSeparate(s, s) || databasePinsSeparate(s, databaseIdentityPin{"123", 100, 201}) {
		t.Fatal("same database with other role accepted")
	}
	if !databasePinsSeparate(s, databaseIdentityPin{"123", 101, 200}) || !databasePinsSeparate(s, databaseIdentityPin{"124", 100, 200}) {
		t.Fatal("separate database denied")
	}
}

func TestDatabaseIdentityRejectsBeforeConnect(t *testing.T) {
	cleanPG(t)
	s, target := databaseIdentityPin{"123", 100, 200}, databaseIdentityPin{"123", 101, 200}
	for _, raw := range []string{"postgres://reader:SECRET@172.16.1.18:5432/yreader_a1_iso_identity", "postgres://reader:SECRET@127.0.0.1:5432/yreader", "bad SECRET"} {
		if h, e := probeHandoverDatabase(context.Background(), raw, s, target); h != "" || e != errDatabaseIdentity {
			t.Fatal("invalid target or sensitive error escaped")
		}
	}
	if h, e := probeHandoverDatabase(context.Background(), "", s, s); h != "" || e != errDatabaseIdentity {
		t.Fatal("alias accepted")
	}
	if h, e := probeHandoverDatabase(nil, "", s, target); h != "" || e != errDatabaseIdentity {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h, e := probeHandoverDatabase(ctx, "postgres://reader:SECRET@127.0.0.1:1/yreader_a1_iso_identity?sslmode=disable", s, target); h != "" || e != errDatabaseIdentity {
		t.Fatal("canceled probe returned partial identity")
	}
}
