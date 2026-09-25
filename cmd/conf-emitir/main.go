// conf-emitir — manda audio a una sala de conf desde la línea de comandos,
// sin navegador. Para el stream de un escenario, una placa de audio en un
// servidor, o un archivo:
//
//	ffmpeg -re -i rtmp://… -f s16le -ar 16000 -ac 1 - | conf-emitir -url https://conf.aaroidev.com -sala sala-1a2b3c4d
//
// El token de emisión va en la variable CONF_TOKEN (o en -token-archivo),
// NUNCA en argv: los argumentos se ven en `ps` y quedan en el historial.
//
// Se recupera solo, como la consola web: si el hub se reinicia o la red se
// corta, reconecta con espera creciente (0,5 s → 5 s) y un vigilante
// abandona una conexión que no late en 5 s. Mientras está desconectado,
// descarta el audio (llegar tarde es peor que perder una frase) y sigue
// leyendo stdin para que ffmpeg no se bloquee.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	bloque      = 3200 // 100 ms de PCM s16le 16 kHz mono
	sinLatido   = 5 * time.Second
	esperaMax   = 5 * time.Second
	esperaDesde = 500 * time.Millisecond
)

func espera(n int) time.Duration {
	d := esperaDesde << min(n, 10)
	return min(d, esperaMax)
}

func urlAudio(base, sala string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("-url inválida: %q", base)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("-url tiene que ser http(s)://…")
	}
	u.Path = "/api/salas/" + url.PathEscape(sala) + "/audio"
	return u.String(), nil
}

func leerToken(archivo string) (string, error) {
	if t := strings.TrimSpace(os.Getenv("CONF_TOKEN")); t != "" {
		return t, nil
	}
	if archivo != "" {
		b, err := os.ReadFile(archivo)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", errors.New("falta el token: CONF_TOKEN=… o -token-archivo (nunca por argv)")
}

var errToken = errors.New("el hub rechazó el token de emisión de esta sala")

// sesion conecta una vez y emite hasta que algo se corta.
func sesion(ctx context.Context, dir, token string, audio <-chan []byte, tramos *atomic.Int64) error {
	ctxDial, cancel := context.WithTimeout(ctx, sinLatido)
	c, _, err := websocket.Dial(ctxDial, dir, nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	ctx, cortar := context.WithCancel(ctx)
	defer cortar()
	hola, _ := json.Marshal(map[string]string{"token": token})
	if err := c.Write(ctx, websocket.MessageText, hola); err != nil {
		return err
	}
	var ultimo atomic.Int64
	ultimo.Store(time.Now().UnixNano())
	listo := make(chan struct{})
	errLectura := make(chan error, 1)
	go func() {
		var una bool
		for {
			_, m, err := c.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
					errLectura <- errToken
				} else {
					errLectura <- err
				}
				return
			}
			ultimo.Store(time.Now().UnixNano())
			var v struct {
				OK     bool  `json:"ok"`
				Tramos int64 `json:"tramos"`
			}
			if json.Unmarshal(m, &v) == nil {
				if v.OK && !una {
					una = true
					close(listo)
				}
				if v.Tramos > 0 {
					tramos.Store(v.Tramos)
				}
			}
		}
	}()
	vigia := time.NewTicker(time.Second)
	defer vigia.Stop()
	emitiendo := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errLectura:
			return err
		case <-listo:
			if !emitiendo {
				emitiendo = true
				log.Printf("emitiendo en %s", dir)
			}
			listo = nil
		case <-vigia.C:
			if time.Since(time.Unix(0, ultimo.Load())) > sinLatido {
				return errors.New("el hub no late hace 5 s: abandono la conexión")
			}
		case b, ok := <-audio:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "fin del audio")
				return io.EOF
			}
			if emitiendo {
				if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
					return err
				}
			}
		}
	}
}

func main() {
	log.SetFlags(log.Ltime)
	base := flag.String("url", "", "dónde está conf: https://conf.aaroidev.com")
	sala := flag.String("sala", "", "id de la sala (lo da el panel)")
	tokenArchivo := flag.String("token-archivo", "", "archivo con el token de emisión (si no está CONF_TOKEN)")
	flag.Parse()
	if *base == "" || *sala == "" {
		fmt.Fprintln(os.Stderr, "uso: ffmpeg … -f s16le -ar 16000 -ac 1 - | CONF_TOKEN=… conf-emitir -url https://… -sala <sala>")
		os.Exit(2)
	}
	dir, err := urlAudio(*base, *sala)
	if err != nil {
		log.Fatal(err)
	}
	token, err := leerToken(*tokenArchivo)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// stdin → bloques de 100 ms. El canal tiene poco margen a propósito:
	// si nadie lo lee (desconectado), el lector descarta en vez de frenar a
	// ffmpeg.
	audio := make(chan []byte, 20)
	go func() {
		defer close(audio)
		for {
			b := make([]byte, bloque)
			if _, err := io.ReadFull(os.Stdin, b); err != nil {
				return
			}
			select {
			case audio <- b:
			default: // desconectado: se descarta, no se acumula atraso
			}
		}
	}()

	var tramos atomic.Int64
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				log.Printf("tramos enviados al motor: %d", tramos.Load())
			}
		}
	}()

	for intento := 0; ; intento++ {
		err := sesion(ctx, dir, token, audio, &tramos)
		switch {
		case errors.Is(err, io.EOF):
			log.Print("fin del audio")
			return
		case ctx.Err() != nil:
			return
		case errors.Is(err, errToken):
			log.Fatal(err)
		}
		d := espera(intento)
		log.Printf("cortado (%v): reconecto en %s", err, d)
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		if err == nil {
			intento = 0
		}
	}
}
