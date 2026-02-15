package util

import (
	"fmt"
	"os"
)

func Getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func MustGetenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("missing env var %s", key))
	}
	return v
}
