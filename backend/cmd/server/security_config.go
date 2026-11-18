package main

import "errors"

func validateJWTSecret(secret, environment string) error {
	if environment == "production" && len([]byte(secret)) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes in production")
	}
	return nil
}
