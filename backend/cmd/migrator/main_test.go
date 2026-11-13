package main

import "testing"

func TestExplicitTarget(t *testing.T) {
	for _, raw := range []string{"host=127.0.0.1 port=5432 dbname=yreader_test password='a b!@'", "postgres://u:p@127.0.0.1:5432/yreader_test"} {
		if !explicitTarget(raw) {
			t.Fatal("valid target rejected")
		}
	}
	for _, raw := range []string{"dbname=yreader_test password='host=x port=1'", "host=127.0.0.1 port=5432 dbname=yreader_test host=other", "postgres://localhost/yreader_test", "host=localhost port=5432 dbname=yreader_test service=x"} {
		if explicitTarget(raw) {
			t.Fatal("ambiguous target accepted")
		}
	}
}
