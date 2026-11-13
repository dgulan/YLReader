package parser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run untrusted boolean selectors out of process: a regression must not leave a
// CPU-bound goroutine alive in the test runner. CommandContext kills and Waits.
func TestXPathBooleanSelectorsTerminate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestXPathTerminationWorker$")
	cmd.Env = append(os.Environ(), "YREADER_XPATH_TEST_WORKER=1")
	output, err := cmd.CombinedOutput()
	if !strings.Contains(string(output), "xpath-worker-ready") {
		t.Fatalf("worker did not enter evaluator: %s (%v)", output, err)
	}
	if err != nil {
		t.Fatalf("XPath evaluator did not terminate cleanly: %v, %s", err, output)
	}
}

func TestXPathTerminationWorker(t *testing.T) {
	if os.Getenv("YREADER_XPATH_TEST_WORKER") != "1" {
		t.Skip("subprocess only")
	}
	fmt.Println("xpath-worker-ready")
	for _, selector := range []string{"1=1", "true()", "false()"} {
		if _, err := evalXPath(selector, "<html><body><p>text</p></body></html>"); err != nil {
			t.Fatal(err)
		}
		if _, err := evalListNodesXPath(selector, "<html><body><p>text</p></body></html>"); err != nil {
			t.Fatal(err)
		}
	}
}
