package main

import (
	"fmt"
	"strings"
	"time"
)

func parseImportTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 90 * time.Second, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < time.Second || d > 10*time.Minute {
		return 0, fmt.Errorf("LOCAL_IMPORT_TIMEOUT must be a duration between 1s and 10m")
	}
	return d, nil
}
