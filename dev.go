//go:build dev

package main

// Sólo con `go run -tags dev .`: levanta un redis en memoria (miniredis) en
// REDIS_DEV_ADDR (defecto 127.0.0.1:6379) para correr la pila entera en una
// máquina sin redis ni contenedores. La imagen se construye SIN este tag:
// ahí el redis es el servicio `bus` de la plataforma.

import (
	"log/slog"
	"os"

	"github.com/alicebob/miniredis/v2"
)

func init() {
	antesDeArrancar = func(cfg *Config) {
		m := miniredis.NewMiniRedis()
		addr := entorno("REDIS_DEV_ADDR", "127.0.0.1:6379")
		if err := m.StartAddr(addr); err != nil {
			slog.Error("miniredis", "err", err)
			os.Exit(1)
		}
		cfg.RedisURL = "redis://" + addr + "/0"
		if cfg.ClaveFirma == "" {
			cfg.ClaveFirma = "clave-de-desarrollo-no-usar"
		}
		if cfg.OperadorUsuario == "" {
			cfg.OperadorUsuario, cfg.OperadorPassword = "operador", "operador"
		}
		cfg.CookieSegura = false
		slog.Warn("MODO DEV: redis en memoria, operador/operador, cookie sin Secure", "redis", addr)
	}
}
