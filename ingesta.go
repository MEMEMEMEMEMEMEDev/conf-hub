package main

// Cómo entra el audio de una sala. Dos fuentes, el mismo camino:
//
//   - la consola /emitir del navegador (micrófono o pestaña) por WebSocket:
//     primer mensaje de texto {"token": "…"}, después frames binarios de
//     PCM s16le 16 kHz mono. El token va en un mensaje y no en la URL
//     porque las URLs terminan en los logs del borde.
//   - la fuente DEMO: los dos fragmentos de Nerdearla 2025 en loop, a ritmo
//     real, marcados como grabados en todas las vistas.
//
// Las dos alimentan un Segmentador, y los tramos que salen van a redis.

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

//go:embed demo/*.pcm
var demoFS embed.FS

// Charla es una pista del catálogo de salas demo (demo/FUENTES.md).
type Charla struct {
	Archivo, Idioma, Titulo, Orador string
	// Nombres propios y marcas QUE SE DICEN en ese tramo: se escriben así
	// y no se traducen (el motor los protege en la traducción). Nada de
	// palabras comunes: "piratería" o "cutlasses" quedarían sin traducir.
	Glosario string
}

var Catalogo = []Charla{
	{"en.pcm", "en", "Multilingual AI agents", "Thor Schaeff", "ElevenLabs, HDMI, Thor Schaeff"},
	{"es.pcm", "es", "Programming is dead", "midudev", "Midudev, Midu.dev, Miguel Ángel Durán, Javier Tebas, la Liga, Instagram, Nerdearla"},
	{"d567RerxDGk.pcm", "en", "Interview with Andrew S. Tanenbaum", "Andrew S. Tanenbaum", "Andrew Tanenbaum, Nicolás Wolovick, Unix, MINIX, AT&T, John Lions, Butler Lampson, Berkeley, CTSS, Ken Thompson, Dennis Ritchie"},
	{"ObElMurNobI.pcm", "es", "El futuro de la IA", "Omar Sanseviero", "Omar Sanseviero, Google DeepMind, Gemini 2.5 Flash Image, Gemini, Nano Banana, Veo"},
	{"GkVjMxYi5gA.pcm", "en", "Conversational AI agents with ElevenLabs", "Thor Schaeff", "ElevenLabs, Thor Schaeff, Conversational AI, Gemini, Nerdearla"},
	{"nGeiH6GSIuU.pcm", "es", "No sos Netflix", "J. Rodríguez Monti", "Netflix, Rodríguez Monti"},
	{"rfR_UUkB7cI.pcm", "es", "La IA me obliga a pensar más", "Manu Rodríguez Gil", "Manu Rodríguez Gil, React, Angular, Vue"},
	{"MyKj87N5B7Y.pcm", "es", "10 comandos para cambiarte la vida", "Emiliano Carletti", "Emiliano Carletti, Linux, macOS, Windows, Bash, Zsh, Xcode"},
	{"D-L9zRwX1go.pcm", "es", "20 años de SRE", "Nerdearla 2025", "SRE, Linux, LUGMen, Mendoza, ChatGPT, Nerdearla"},
}

func bytesAPCM(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// publicador arma el callback del segmentador de una sala: relee la sala
// (backend e idioma pueden cambiar desde el panel) y publica el tramo.
func (h *Hub) publicador(ctx context.Context, salaID string) func(Tramo) {
	v := h.vivo(salaID)
	return func(t Tramo) {
		s, err := h.bus.Sala(ctx, salaID)
		if err != nil {
			slog.Warn("tramo sin sala", "sala", salaID, "err", err)
			return
		}
		if err := h.bus.PublicarTramo(ctx, s, t); err != nil {
			slog.Warn("no pude publicar el tramo", "sala", salaID, "err", err)
			return
		}
		v.mu.Lock()
		if t.Final {
			v.Tramos++
		} else {
			v.Parciales++
		}
		v.mu.Unlock()
	}
}

func (h *Hub) alimentar(seg *Segmentador, salaID string, pcm []int16) {
	seg.Escribir(pcm)
	v := h.vivo(salaID)
	v.mu.Lock()
	v.UltimoAudio = h.ahora()
	v.Nivel = seg.Nivel
	v.mu.Unlock()
}

// ---- WebSocket de emisión ---------------------------------------------------

type emisiones struct {
	mu     sync.Mutex
	gen    int64
	activa map[string]emisionActiva
}

type emisionActiva struct {
	gen    int64
	cancel context.CancelFunc
}

// tomar registra la conexión nueva de una sala y corta la anterior: dos
// emisores en la misma sala mezclarían su audio en los mismos tramos. El
// último que llega manda (es el que el operador acaba de abrir).
func (e *emisiones) tomar(sala string, cancel context.CancelFunc) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if prev, ok := e.activa[sala]; ok {
		prev.cancel()
	}
	e.gen++
	e.activa[sala] = emisionActiva{gen: e.gen, cancel: cancel}
	return e.gen
}

// soltar borra la entrada sólo si sigue siendo la suya: si otro emisor ya
// la reemplazó, la del nuevo no se toca.
func (e *emisiones) soltar(sala string, gen int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.activa[sala]; ok && a.gen == gen {
		delete(e.activa, sala)
	}
}

type holaEmisor struct {
	Token string `json:"token"`
}

func (a *API) audio(w http.ResponseWriter, r *http.Request) {
	salaID := r.PathValue("id")
	sala, err := a.hub.bus.Sala(r.Context(), salaID)
	if errors.Is(err, ErrNoExiste) {
		jsonError(w, http.StatusNotFound, "sin_sala", "la sala no existe")
		return
	}
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	if sala.Demo {
		jsonError(w, http.StatusConflict, "sala_demo", "las salas demo emiten solas")
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.hub.cfg.Origenes})
	if err != nil {
		return // Accept ya respondió
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// El primer mensaje es la llave. Cinco segundos para mandarla.
	ctxHola, cancelHola := context.WithTimeout(ctx, 5*time.Second)
	tipo, datos, err := c.Read(ctxHola)
	cancelHola()
	var hola holaEmisor
	if err != nil || tipo != websocket.MessageText || json.Unmarshal(datos, &hola) != nil ||
		!a.firma.TokenValido(salaID, hola.Token) {
		c.Close(websocket.StatusPolicyViolation, "token de emisión inválido")
		return
	}

	gen := a.emisiones.tomar(salaID, cancel)
	defer a.emisiones.soltar(salaID, gen)

	v := a.hub.vivo(salaID)
	v.mu.Lock()
	v.Emisores++
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.Emisores--
		v.mu.Unlock()
	}()

	seg := NuevoSegmentador(a.hub.cfg.Segmentador, a.hub.ahora(), a.hub.publicador(ctx, salaID))
	defer seg.Vaciar()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"ok":true}`))

	// Latido hacia el emisor: nivel y tramos, cada segundo. Además mantiene
	// viva la conexión a través del borde.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r := a.hub.Resumen(salaID)
				b, _ := json.Marshal(map[string]any{"nivel": r.Nivel, "tramos": r.Tramos,
					"lat_p50_ms": r.P50})
				if c.Write(ctx, websocket.MessageText, b) != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		tipo, datos, err := c.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !strings.Contains(err.Error(), "EOF") &&
				websocket.CloseStatus(err) == -1 {
				slog.Info("emisor desconectado", "sala", salaID, "err", err)
			}
			return
		}
		if tipo != websocket.MessageBinary || len(datos)%2 != 0 {
			continue
		}
		a.hub.alimentar(seg, salaID, bytesAPCM(datos))
	}
}

// ---- fuente demo ------------------------------------------------------------

// DemoFuente reproduce una pista en loop, a ritmo real, sobre una sala.
// Sólo manda tramos al motor mientras alguien mira la sala (si
// SoloConPublico): el reloj de la pista avanza igual, así que quien llega
// entra en medio de la charla como en una sala de verdad, y una sala demo
// sin nadie mirando no gasta GPU ni —si el motor cayó a Gemini— dinero.
func (h *Hub) DemoFuente(ctx context.Context, salaID string, pista []int16, desfase time.Duration, soloConPublico bool) {
	const paso = 100 * time.Millisecond
	muestras := int(paso.Seconds() * FrecMuestreo)
	pos := int(desfase.Seconds()*FrecMuestreo) % len(pista)
	pos -= pos % muestras

	v := h.vivo(salaID)
	v.mu.Lock()
	v.Emisores++
	v.mu.Unlock()

	seg := NuevoSegmentador(h.cfg.Segmentador, h.ahora(), h.publicador(ctx, salaID))
	d := h.difusor(salaID)
	callado := false

	t := time.NewTicker(paso)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		trozo := make([]int16, muestras)
		for i := range trozo {
			trozo[i] = pista[(pos+i)%len(pista)]
		}
		pos = (pos + muestras) % len(pista)

		if soloConPublico && d.Espectadores() == 0 {
			if !callado {
				seg.Vaciar()
				callado = true
			}
			// El segmentador sigue midiendo nivel para el panel, pero no
			// recibe el audio: al volver alguien, arranca limpio.
			v.mu.Lock()
			v.UltimoAudio = h.ahora()
			v.Nivel = energiaDB(trozo)
			v.mu.Unlock()
			continue
		}
		if callado {
			seg = NuevoSegmentador(h.cfg.Segmentador, h.ahora(), h.publicador(ctx, salaID))
			callado = false
		}
		h.alimentar(seg, salaID, trozo)
	}
}

// idDemo: a, b, …, z, y después 27, 28… (a y b siguen siendo las de siempre).
func idDemo(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	return fmt.Sprintf("%02d", i+1)
}

// PrepararDemo crea las salas demo que falten y lanza sus fuentes.
func (h *Hub) PrepararDemo(ctx context.Context) error {
	if h.cfg.SalasDemo == 0 {
		return nil
	}
	pcms := map[string][]int16{}
	for _, c := range Catalogo {
		b, err := demoFS.ReadFile("demo/" + c.Archivo)
		if err != nil {
			return fmt.Errorf("demo/%s: %w", c.Archivo, err)
		}
		pcms[c.Archivo] = bytesAPCM(b)
	}
	for i := 0; i < h.cfg.SalasDemo; i++ {
		c := Catalogo[i%len(Catalogo)]
		p := struct{ pcm []int16 }{pcms[c.Archivo]}
		id := fmt.Sprintf("demo-%s", idDemo(i))
		nombre := fmt.Sprintf("Sala %02d · %s · %s", i+1, c.Titulo, c.Orador)
		s, err := h.bus.Sala(ctx, id)
		if errors.Is(err, ErrNoExiste) {
			s = Sala{ID: id, Nombre: nombre, Idioma: c.Idioma, Backend: h.cfg.BackendDefecto,
				Demo: true, Creada: h.ahora().Unix(), Glosario: c.Glosario}
			if err := h.bus.GuardarSala(ctx, s); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if s.Nombre != nombre || s.Idioma != c.Idioma || s.Glosario != c.Glosario {
			// Las salas demo las maneja el hub: nombre, idioma y glosario
			// salen del catálogo (si cambió, se actualizan). El backend que
			// eligió producción desde el panel se respeta.
			s.Nombre, s.Idioma, s.Glosario = nombre, c.Idioma, c.Glosario
			if err := h.bus.GuardarSala(ctx, s); err != nil {
				return err
			}
		}
		// Desfase distinto por sala: que dos salas con la misma pista no
		// digan lo mismo a la vez.
		go h.DemoFuente(ctx, s.ID, p.pcm, time.Duration((i/len(Catalogo))*47)*time.Second, h.cfg.DemoSoloConPublico)
	}
	return nil
}
