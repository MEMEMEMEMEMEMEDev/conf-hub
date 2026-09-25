package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Hub junta lo que vive en memoria de este proceso: un difusor por sala y
// el estado de la emisión de cada una. Es UNA réplica a propósito: lo
// durable está en redis, y lo que está acá se reconstruye solo al arrancar
// (los difusores releen desde el último id; los emisores se reconectan).
type Hub struct {
	bus   *Bus
	cfg   Config
	ahora func() time.Time

	mu        sync.Mutex
	difusores map[string]*Difusor
	vivos     map[string]*Vivo
	lectores  map[string]context.CancelFunc
	raiz      context.Context

	reacciones Reacciones
}

func NuevoHub(bus *Bus, cfg Config) *Hub {
	return &Hub{bus: bus, cfg: cfg, ahora: time.Now,
		difusores: map[string]*Difusor{}, vivos: map[string]*Vivo{},
		lectores:   map[string]context.CancelFunc{},
		reacciones: Reacciones{cuenta: map[string]map[string]int{}}}
}

// ---- estado vivo de una sala (para el panel y los eventos de estado) ----

type Vivo struct {
	mu          sync.Mutex
	Emisores    int       // conexiones de audio abiertas (demo cuenta como una)
	UltimoAudio time.Time // último PCM recibido
	Nivel       float64   // dBFS
	Tramos      int64     // tramos finales enviados al motor
	Parciales   int64
	Latencias   []int64 // ms, últimos finales
	Saltados    int64
	Errores     int64
	UltimoError string
	UltimoFinal time.Time
	// El backend que ATENDIÓ el último final, no el que la sala pide: si la
	// GPU se cae y el motor pasa la sala a Gemini, el panel y la audiencia
	// lo ven acá.
	Atendio string
	// Cuándo alguien tocó "no se entiende" (se recorta a los últimos 2 min).
	NoSeEntiende []time.Time
}

func (h *Hub) vivo(sala string) *Vivo {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.vivos[sala]
	if !ok {
		v = &Vivo{Nivel: -90}
		h.vivos[sala] = v
	}
	return v
}

type ResumenVivo struct {
	EnVivo      bool    `json:"en_vivo"`
	Emisores    int     `json:"emisores"`
	HaceAudioS  float64 `json:"hace_audio_s"`
	Nivel       float64 `json:"nivel"`
	Tramos      int64   `json:"tramos"`
	Parciales   int64   `json:"parciales"`
	P50         int64   `json:"lat_p50_ms"`
	P95         int64   `json:"lat_p95_ms"`
	Muestras    int     `json:"lat_muestras"`
	Saltados    int64   `json:"saltados"`
	Errores     int64   `json:"errores"`
	UltimoError string  `json:"ultimo_error,omitempty"`
	HaceFinalS  float64 `json:"hace_final_s"`
	Atendio     string  `json:"atendio"`
}

func percentil(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	c := slices.Clone(xs)
	slices.Sort(c)
	i := int(p*float64(len(c)-1) + 0.5)
	return c[i]
}

func (h *Hub) Resumen(sala string) ResumenVivo {
	v := h.vivo(sala)
	v.mu.Lock()
	defer v.mu.Unlock()
	ahora := h.ahora()
	r := ResumenVivo{Emisores: v.Emisores, Nivel: v.Nivel, Tramos: v.Tramos, Parciales: v.Parciales,
		P50: percentil(v.Latencias, 0.5), P95: percentil(v.Latencias, 0.95), Muestras: len(v.Latencias),
		Saltados: v.Saltados, Errores: v.Errores, UltimoError: v.UltimoError, HaceAudioS: -1, HaceFinalS: -1,
		Atendio: v.Atendio}
	if !v.UltimoAudio.IsZero() {
		r.HaceAudioS = ahora.Sub(v.UltimoAudio).Seconds()
	}
	if !v.UltimoFinal.IsZero() {
		r.HaceFinalS = ahora.Sub(v.UltimoFinal).Seconds()
	}
	// En vivo = alguien está mandando audio AHORA, no "hubo una conexión".
	r.EnVivo = v.Emisores > 0 && r.HaceAudioS >= 0 && r.HaceAudioS < 5
	return r
}

func (v *Vivo) registrarSub(s Subtitulo, ahora time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch s.Tipo {
	case "final":
		v.Latencias = append(v.Latencias, s.LatMs)
		if len(v.Latencias) > 200 {
			v.Latencias = v.Latencias[len(v.Latencias)-200:]
		}
		v.UltimoFinal = ahora
		v.Atendio = s.Backend
	case "saltado":
		v.Saltados++
	case "error":
		v.Errores++
		v.UltimoError = s.Detalle
	}
}

// ---- difusor: UN lector de redis por sala, N espectadores ----------------
//
// Sin esto, cada espectador sería un XREAD BLOCK propio contra redis: con
// trescientas personas mirando una sala son trescientas conexiones
// bloqueadas. Acá hay una por sala, y reparte en memoria.

// Mensaje es lo que un difusor reparte: un subtítulo del stream de redis
// (con id, se puede retomar) o un evento de la sala (reacciones, preguntas:
// sin id, es el estado de ahora y no una historia que haya que recuperar).
type Mensaje struct {
	Sub    *Subtitulo
	Evento string
	Datos  any
}

type Difusor struct {
	sala string
	mu   sync.Mutex
	subs map[chan Mensaje]struct{}
}

func (h *Hub) difusor(sala string) *Difusor {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.difusores[sala]
	if !ok {
		d = &Difusor{sala: sala, subs: map[chan Mensaje]struct{}{}}
		h.difusores[sala] = d
	}
	return d
}

func (d *Difusor) Suscribir() chan Mensaje {
	c := make(chan Mensaje, 64)
	d.mu.Lock()
	d.subs[c] = struct{}{}
	d.mu.Unlock()
	return c
}

func (d *Difusor) Desuscribir(c chan Mensaje) {
	d.mu.Lock()
	if _, ok := d.subs[c]; ok {
		delete(d.subs, c)
		close(c)
	}
	d.mu.Unlock()
}

func (d *Difusor) Espectadores() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.subs)
}

func (d *Difusor) repartirMensaje(m Mensaje) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for c := range d.subs {
		select {
		case c <- m:
		default:
			// Un espectador que no lee (pestaña dormida, red que se
			// cayó sin avisar) no puede frenar a los demás: se lo
			// suelta y su EventSource se reconecta con Last-Event-ID
			// sin perder nada.
			delete(d.subs, c)
			close(c)
		}
	}
}

func (d *Difusor) repartir(s Subtitulo) { d.repartirMensaje(Mensaje{Sub: &s}) }

// Emitir reparte un evento de la sala a quienes la miran ahora.
func (d *Difusor) Emitir(evento string, datos any) {
	d.repartirMensaje(Mensaje{Evento: evento, Datos: datos})
}

// Leer corre mientras viva ctx y reparte todo lo posterior a desde.
func (h *Hub) Leer(ctx context.Context, sala, desde string) {
	d := h.difusor(sala)
	v := h.vivo(sala)
	id := desde
	for ctx.Err() == nil {
		subs, err := h.bus.Esperar(ctx, sala, id, 5*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("difusor: redis", "sala", sala, "err", err)
			time.Sleep(time.Second)
			continue
		}
		for _, s := range subs {
			id = s.ID
			v.registrarSub(s, h.ahora())
			d.repartir(s)
		}
	}
}

// AsegurarLector garantiza que la sala tenga su lector, y lo arranca de
// forma SÍNCRONA en el último id existente. El orden importa y es la razón
// de esta función: el handler del SSE se suscribe, llama acá, y recién
// después hace el replay. Todo lo anterior al punto de partida lo trae el
// replay; todo lo posterior, el lector. Sin hueco entre los dos (el bug
// que tenía la versión que sólo revisaba salas cada 2 s: lo publicado en
// una sala nueva antes de la primera vuelta no le llegaba a nadie).
func (h *Hub) AsegurarLector(sala string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.lectores[sala]; ok || h.raiz == nil {
		return nil
	}
	c, cancel := context.WithTimeout(h.raiz, 3*time.Second)
	desde, err := h.bus.UltimoID(c, sala)
	cancel()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(h.raiz)
	h.lectores[sala] = cancel
	go h.Leer(ctx, sala, desde)
	return nil
}

func (h *Hub) soltarLector(sala string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cancel, ok := h.lectores[sala]; ok {
		cancel()
		delete(h.lectores, sala)
	}
}

// Arrancar fija el contexto raíz y mantiene un lector por sala existente
// (las que se crean o borran desde otro proceso se ven en la vuelta).
func (h *Hub) Arrancar(ctx context.Context) {
	h.mu.Lock()
	h.raiz = ctx
	h.mu.Unlock()
	revisar := func() {
		salas, err := h.bus.Salas(ctx)
		if err != nil {
			slog.Warn("no pude listar salas", "err", err)
			return
		}
		existe := map[string]bool{}
		for _, s := range salas {
			existe[s.ID] = true
			if err := h.AsegurarLector(s.ID); err != nil {
				slog.Warn("lector", "sala", s.ID, "err", err)
			}
		}
		h.mu.Lock()
		var sobran []string
		for id := range h.lectores {
			if !existe[id] {
				sobran = append(sobran, id)
			}
		}
		h.mu.Unlock()
		for _, id := range sobran {
			h.soltarLector(id)
		}
	}
	revisar()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			revisar()
		}
	}
}
