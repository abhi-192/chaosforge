package util

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func InitLogger(service string) {
	level := strings.ToLower(os.Getenv("LOG_LEVEL"))
	lvl := zerolog.InfoLevel
	switch level {
	case "debug":
		lvl = zerolog.DebugLevel
	case "warn":
		lvl = zerolog.WarnLevel
	case "error":
		lvl = zerolog.ErrorLevel
	}
	zerolog.SetGlobalLevel(lvl)
	log.Logger = log.With().Str("service", service).Logger()
}
