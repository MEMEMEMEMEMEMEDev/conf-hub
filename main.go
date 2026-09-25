// conf-hub — la puerta HTTP de conf: recibe el audio de cada sala, lo corta
// en tramos, se los pasa al motor por redis y reparte los subtítulos a la
// audiencia por SSE. También el panel de producción y el export.
//
// No transcribe nada: eso es conf-motor, en la GPU. Este proceso es Go sin
// más dependencias que el cliente de redis y el de WebSocket, para que su
// imagen sea chica y su escaneo no tenga de qué quejarse.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Puerto             string
	RedisURL           string
	ClaveFirma         string
	OperadorUsuario    string
	OperadorPassword   string
	CookieSegura       bool
	ConfiarCF          bool // detrás del túnel de Cloudflare: CF-Connecting-IP es la IP real
	BackendDefecto     string
	SalasDemo          int
	DemoSoloConPublico bool
	MaxSalas           int
	Origenes           []string
	Latido             time.Duration
	Segmentador        ConfigSegmentador
}

func entorno(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func entornoInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return def
}

func entornoSeg(k string, def time.Duration) time.Duration {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second))
	}
	return def
}

func CargarConfig() Config {
	seg := ConfigPorDefecto()
	seg.Min = entornoSeg("TRAMO_MIN_S", seg.Min)
	seg.Max = entornoSeg("TRAMO_MAX_S", seg.Max)
	seg.CadaParcial = entornoSeg("PARCIAL_CADA_S", 0)
	var origenes []string
	if o := os.Getenv("ORIGENES"); o != "" {
		origenes = strings.Split(o, ",")
	}
	return Config{
		Puerto:             entorno("PORT", "8080"),
		RedisURL:           urlRedis(),
		ClaveFirma:         claveFirma(),
		OperadorUsuario:    os.Getenv("OPERADOR_USUARIO"),
		OperadorPassword:   os.Getenv("OPERADOR_PASSWORD"),
		CookieSegura:       entorno("COOKIE_SEGURA", "1") == "1",
		ConfiarCF:          entorno("CONFIAR_CF", "0") == "1",
		BackendDefecto:     entorno("BACKEND_DEFECTO", "gpu"),
		SalasDemo:          entornoInt("SALAS_DEMO", 2),
		DemoSoloConPublico: entorno("DEMO_SOLO_CON_PUBLICO", "1") == "1",
		MaxSalas:           entornoInt("MAX_SALAS", 28),
		Origenes:           origenes,
		Latido:             entornoSeg("LATIDO_S", 15*time.Second),
		Segmentador:        seg,
	}
}

// urlRedis: REDIS_URL si está; si no, REDIS_ADDR + REDIS_PASSWORD, que es
// como la plataforma lo entrega (servicio `bus`, Secret bus-credenciales):
// una contraseña no se puede meter en una URL desde un secretKeyRef.
func urlRedis() string {
	if u := os.Getenv("REDIS_URL"); u != "" {
		return u
	}
	addr := entorno("REDIS_ADDR", "localhost:6379")
	if pw := os.Getenv("REDIS_PASSWORD"); pw != "" {
		return "redis://:" + url.QueryEscape(pw) + "@" + addr + "/0"
	}
	return "redis://" + addr + "/0"
}

// claveFirma: HUB_CLAVE_FIRMA si está. Si no, se DERIVA de la contraseña de
// redis, que es un secreto que la plataforma ya entrega cifrado, estable
// entre reinicios y que nunca sale del clúster: los enlaces de emisión y las
// sesiones del panel sobreviven a un despliegue sin pedir otro secreto.
func claveFirma() string {
	if k := os.Getenv("HUB_CLAVE_FIRMA"); k != "" {
		return k
	}
	if pw := os.Getenv("REDIS_PASSWORD"); pw != "" {
		h := sha256.Sum256([]byte("conf-hub/firma/v1:" + pw))
		return hex.EncodeToString(h[:])
	}
	return ""
}

func ordenarSalas(s []SalaPublica) {
	slices.SortFunc(s, func(a, b SalaPublica) int {
		if a.Demo != b.Demo {
			if a.Demo {
				return 1
			}
			return -1
		}
		if a.EnVivo != b.EnVivo {
			if a.EnVivo {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Nombre, b.Nombre)
	})
}

// antesDeArrancar lo sobreescribe dev.go (build tag dev) para levantar un
// redis en memoria. En la imagen no existe: ahí redis es el de la plataforma.
var antesDeArrancar = func(cfg *Config) {}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := CargarConfig()
	antesDeArrancar(&cfg)

	// Sin clave de firma no hay tokens de emisión ni sesiones: arrancar
	// así sería servir una sala que cualquiera puede secuestrar.
	if len(cfg.ClaveFirma) < 16 {
		slog.Error("sin clave de firma: poné HUB_CLAVE_FIRMA (≥ 16 caracteres) o REDIS_PASSWORD")
		os.Exit(1)
	}

	bus, err := NuevoBus(cfg.RedisURL)
	if err != nil {
		slog.Error("redis", "err", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// Redis puede tardar en levantar junto con el hub: se espera, acotado.
	for i := 0; ; i++ {
		c, cc := context.WithTimeout(ctx, 2*time.Second)
		err = bus.Ping(c)
		cc()
		if err == nil {
			break
		}
		if i == 30 {
			slog.Error("redis no responde", "err", err)
			os.Exit(1)
		}
		time.Sleep(time.Second)
	}

	hub := NuevoHub(bus, cfg)
	if err := hub.PrepararDemo(ctx); err != nil {
		slog.Error("salas demo", "err", err)
		os.Exit(1)
	}
	go hub.Arrancar(ctx)
	go hub.RepartirReacciones(ctx, time.Second)

	api := NuevaAPI(hub)
	srv := &http.Server{
		Addr:              ":" + cfg.Puerto,
		Handler:           registro(api.Rutas()),
		ReadHeaderTimeout: 10 * time.Second,
		// Sin WriteTimeout a propósito: el SSE y el WebSocket son
		// conexiones que duran lo que dura una charla.
	}
	go func() {
		<-ctx.Done()
		c, cc := context.WithTimeout(context.Background(), 5*time.Second)
		defer cc()
		srv.Shutdown(c)
	}()
	slog.Info("conf-hub escuchando", "puerto", cfg.Puerto, "salas_demo", cfg.SalasDemo,
		"tramo_min", cfg.Segmentador.Min.String(), "tramo_max", cfg.Segmentador.Max.String(),
		"parciales", cfg.Segmentador.CadaParcial.String())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http", "err", err)
		os.Exit(1)
	}
}

// registro: una línea por petición, sin query (puede llevar ids de
// reconexión) y sin nada del cuerpo.
func registro(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		h.ServeHTTP(w, r)
		if r.URL.Path == "/api/healthz" || r.URL.Path == "/api/listo" || r.URL.Path == "/api/metrics" {
			return
		}
		slog.Info("peticion", "metodo", r.Method, "ruta", r.URL.Path, "ms", time.Since(t).Milliseconds())
	})
}
