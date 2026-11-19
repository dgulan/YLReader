package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"yreader/backend/internal/engine/sandbox"
)

const (
	minSandboxInputLimitMB      = 1
	maxSandboxInputLimitMB      = 32
	defaultSandboxInputLimitMB  = 32
	defaultSandboxInflightMB    = 64
	maxSandboxInflightMB        = 4096
	minSandboxMaxConcurrent     = 1
	maxSandboxMaxConcurrent     = 32
	defaultSandboxMaxConcurrent = 4
)

func isDevelopmentEnv(environment string) bool {
	return strings.TrimSpace(environment) == "development"
}

func validateJWTSecret(secret, environment string) error {
	if !isDevelopmentEnv(environment) && len([]byte(secret)) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes unless ENVIRONMENT=development")
	}
	return nil
}

func validateRuntimeEnv(environment, storageDir, sandboxSockets string) error {
	if isDevelopmentEnv(environment) {
		return nil
	}
	if strings.TrimSpace(storageDir) == "" {
		return errors.New("STORAGE_DIR is required unless ENVIRONMENT=development")
	}
	if strings.TrimSpace(sandboxSockets) == "" {
		return errors.New("YREADER_SANDBOX_SOCKETS is required unless ENVIRONMENT=development")
	}
	return nil
}

func parseBoundedInt(raw string, def, minVal, maxVal int, envName string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return def, nil
	}
	val, err := strconv.Atoi(trimmed)
	if err != nil || val < minVal || val > maxVal {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", envName, minVal, maxVal)
	}
	return val, nil
}

func parseSandboxConfig(inputLimitMBRaw, inflightMBRaw, maxConcurrentRaw string) (sandbox.Config, error) {
	inputMB, err := parseBoundedInt(inputLimitMBRaw, defaultSandboxInputLimitMB, minSandboxInputLimitMB, maxSandboxInputLimitMB, "YREADER_SANDBOX_INPUT_LIMIT_MB")
	if err != nil {
		return sandbox.Config{}, err
	}
	inflightMB, err := parseBoundedInt(inflightMBRaw, defaultSandboxInflightMB, 1, maxSandboxInflightMB, "YREADER_SANDBOX_INFLIGHT_MB")
	if err != nil {
		return sandbox.Config{}, err
	}
	if inflightMB < 2*inputMB {
		return sandbox.Config{}, fmt.Errorf("YREADER_SANDBOX_INFLIGHT_MB (%d) must be at least 2 * YREADER_SANDBOX_INPUT_LIMIT_MB (%d)", inflightMB, 2*inputMB)
	}
	maxConc, err := parseBoundedInt(maxConcurrentRaw, defaultSandboxMaxConcurrent, minSandboxMaxConcurrent, maxSandboxMaxConcurrent, "YREADER_SANDBOX_MAX_CONCURRENT")
	if err != nil {
		return sandbox.Config{}, err
	}
	return sandbox.Config{
		InputLimit:    inputMB << 20,
		InflightBytes: int64(inflightMB) << 20,
		MaxConcurrent: maxConc,
	}, nil
}
