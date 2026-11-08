package config

import (
	"errors"
	"os"
)

type Config struct {
	DatabaseURL string
}

func Load() (Config, error) {
	url := os.Getenv("INDEXRX_DATABASE_URL")
	if url == "" {
		return Config{}, errors.New("INDEXRX_DATABASE_URL is not set")
	}
	return Config{DatabaseURL: url}, nil
}
