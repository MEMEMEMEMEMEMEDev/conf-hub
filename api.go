package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type API struct {
	hub       *Hub
	firma     Firma
	emisiones *emisiones
	limite    *Limitador
}

func NuevaAPI(h *Hub) *API {
	return &API{hub: h, firma: Firma{clave: []byte(h.cfg.ClaveFirma)},
		emisiones: &emisiones{activa: map[string]emisionActiva{}}, limite: NuevoLimitador()}
}

// Todo cuelga de /api: la plataforma rutea /api al hub y / al front
// estático (contrato de conf, D-02).
func (a *API) Rutas() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/healthz", a.healthz)
	m.HandleFunc("GET /api/listo", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	m.HandleFunc("GET /api/metrics", a.metricas)

	m.HandleFunc("GET /api/salas", a.listarSalas)
	m.HandleFunc("GET /api/salas/{id}", a.verSala)
	m.HandleFunc("GET /api/salas/{id}/subtitulos", a.subtitulos)
	m.HandleFunc("GET /api/salas/{id}/audio", a.audio)
	m.HandleFunc("GET /api/salas/{id}/export", a.exportar)

	// La audiencia (con tope por visitante).
	m.HandleFunc("POST /api/salas/{id}/reacciones", a.reaccionar)
	m.HandleFunc("GET /api/salas/{id}/preguntas", a.listarPreguntas)
	m.HandleFunc("POST /api/salas/{id}/preguntas", a.preguntar)
	m.HandleFunc("POST /api/salas/{id}/preguntas/{pid}/voto", a.votar)
	m.HandleFunc("POST /api/salas/{id}/sugerencias", a.sugerir)

	m.HandleFunc("POST /api/sesion", a.entrar)
	m.HandleFunc("DELETE /api/sesion", a.salir)
	m.HandleFunc("GET /api/sesion", a.soloOperador(func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("GET /api/estado", a.soloOperador(a.estado))
	m.HandleFunc("POST /api/salas", a.soloOperador(a.crearSala))
	m.HandleFunc("PATCH /api/salas/{id}", a.soloOperador(a.editarSala))
	m.HandleFunc("DELETE /api/salas/{id}", a.soloOperador(a.borrarSala))
	m.HandleFunc("GET /api/salas/{id}/token", a.soloOperador(a.tokenSala))
	m.HandleFunc("PATCH /api/salas/{id}/preguntas/{pid}", a.soloOperador(a.moderar))
	m.HandleFunc("GET /api/bandeja", a.soloOperador(a.bandeja))
	m.HandleFunc("GET /api/salas/{id}/sugerencias", a.soloOperador(a.listarSugerencias))
	m.HandleFunc("POST /api/salas/{id}/sugerencias/resolver", a.soloOperador(a.resolverSugerencia))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		jsonError(w, http.StatusNotFound, "no_existe", "ruta desconocida")
	})
	// La sonda de la plantilla pregunta /healthz: es la del PROCESO (lo
	// mismo que /api/listo). La del sitio con motor es /api/healthz.
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	m.Handle("/", front())
	return m
}

// ---- utilidades ---------------------------------------------------------------

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, error, mensaje string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": error, "mensaje": mensaje})
}

var (
	idValido      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	backendValido = map[string]bool{"gpu": true, "cpu": true, "gemini": true}
	idiomaValido  = map[string]bool{"en": true, "es": true}
)

func (a *API) salaDe(w http.ResponseWriter, r *http.Request) (Sala, bool) {
	id := r.PathValue("id")
	if !idValido.MatchString(id) {
		jsonError(w, http.StatusNotFound, "sin_sala", "la sala no existe")
		return Sala{}, false
	}
	s, err := a.hub.bus.Sala(r.Context(), id)
	if errors.Is(err, ErrNoExiste) {
		jsonError(w, http.StatusNotFound, "sin_sala", "la sala no existe")
		return Sala{}, false
	}
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return Sala{}, false
	}
	return s, true
}

// ---- salud ----------------------------------------------------------------------

// healthz dice si hay subtítulos posibles, no sólo si el proceso vive
// (guía 08 §5). 503 si redis no responde o si el motor lleva más de
// MotorVivoHasta sin latir. Un Recreate del motor dura menos que eso: la
// sala queda "degradada" en el panel sin que la sonda la declare muerta.
func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.hub.bus.Ping(ctx); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "redis no responde")
		return
	}
	m, err := a.hub.bus.Motor(ctx, a.hub.ahora())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no pude leer el motor")
		return
	}
	if !m.Vivo {
		jsonError(w, http.StatusServiceUnavailable, "motor", "el motor no late")
		return
	}
	jsonOK(w, map[string]any{"ok": true, "motor": m})
}

// ---- salas (público) ---------------------------------------------------------------

type SalaPublica struct {
	Sala
	EnVivo       bool `json:"en_vivo"`
	Espectadores int  `json:"espectadores"`
}

func (a *API) publica(s Sala) SalaPublica {
	return SalaPublica{Sala: s, EnVivo: a.hub.Resumen(s.ID).EnVivo,
		Espectadores: a.hub.difusor(s.ID).Espectadores()}
}

func (a *API) listarSalas(w http.ResponseWriter, r *http.Request) {
	salas, err := a.hub.bus.Salas(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	out := make([]SalaPublica, 0, len(salas))
	for _, s := range salas {
		out = append(out, a.publica(s))
	}
	// Orden estable: primero las en vivo reales, después las demo, por nombre.
	ordenarSalas(out)
	jsonOK(w, out)
}

func (a *API) verSala(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	jsonOK(w, a.publica(s))
}

// ---- SSE de subtítulos ----------------------------------------------------------------

// comparar ids de stream "ms-seq".
func idMayor(a, b string) bool {
	am, as, _ := strings.Cut(a, "-")
	bm, bs, _ := strings.Cut(b, "-")
	x, _ := strconv.ParseInt(am, 10, 64)
	y, _ := strconv.ParseInt(bm, 10, 64)
	if x != y {
		return x > y
	}
	p, _ := strconv.ParseInt(as, 10, 64)
	q, _ := strconv.ParseInt(bs, 10, 64)
	return p > q
}

var idStream = regexp.MustCompile(`^\d{1,20}-\d{1,20}$`)

type eventoEstado struct {
	EnVivo    bool   `json:"en_vivo"`
	Demo      bool   `json:"demo"`
	Backend   string `json:"backend"` // el que la sala pide
	Atendio   string `json:"atendio"` // el que atendió el último final
	Motor     string `json:"motor"`   // ok | degradado | caido
	GPU       string `json:"gpu"`
	P50       int64  `json:"lat_p50_ms"`
	Audiencia int    `json:"espectadores"`
}

func (a *API) eventoEstado(ctx context.Context, s Sala) eventoEstado {
	r := a.hub.Resumen(s.ID)
	e := eventoEstado{EnVivo: r.EnVivo, Demo: s.Demo, Backend: s.Backend, Atendio: r.Atendio, P50: r.P50,
		Audiencia: a.hub.difusor(s.ID).Espectadores(), Motor: "caido"}
	if m, err := a.hub.bus.Motor(ctx, a.hub.ahora()); err == nil {
		e.GPU = m.GPU
		switch {
		case m.Vivo && m.HaceSeg < 15:
			e.Motor = "ok"
		case m.Vivo:
			e.Motor = "degradado"
		}
	}
	if actual, err := a.hub.bus.Sala(ctx, s.ID); err == nil {
		e.Backend = actual.Backend
	}
	return e
}

func (a *API) subtitulos(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "sse", "el servidor no puede hacer streaming")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	// no-transform: que nada en el camino (Cloudflare, traefik) comprima
	// ni bufferice. X-Accel-Buffering: el nombre que respetan los proxys.
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	d := a.hub.difusor(s.ID)
	// Suscribirse ANTES del replay: lo que llegue mientras se relee queda
	// en el canal y se deduplica por id, en vez de perderse en el hueco.
	c := d.Suscribir()
	defer d.Desuscribir(c)
	if err := a.hub.AsegurarLector(s.ID); err != nil {
		slog.Warn("sse: lector", "sala", s.ID, "err", err)
	}

	enviar := func(evento, id string, datos any) bool {
		b, _ := json.Marshal(datos)
		var err error
		if id != "" {
			_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, evento, b)
		} else {
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evento, b)
		}
		fl.Flush()
		return err == nil
	}

	// El reintento del EventSource: 2 s. Un despliegue corta ~3 s.
	fmt.Fprint(w, "retry: 2000\n\n")
	if !enviar("estado", "", a.eventoEstado(ctx, s)) {
		return
	}

	ultimo := r.Header.Get("Last-Event-ID")
	if ultimo == "" {
		ultimo = r.URL.Query().Get("desde")
	}
	var previos []Subtitulo
	var err error
	if ultimo != "" && idStream.MatchString(ultimo) {
		previos, err = a.hub.bus.Desde(ctx, s.ID, ultimo)
	} else {
		// Primera vez: las últimas líneas, para que la pantalla no
		// arranque vacía. Llevan su hora; la vista dice de cuándo son.
		previos, err = a.hub.bus.Ultimos(ctx, s.ID, 4)
		ultimo = "0-0"
	}
	if err != nil {
		slog.Warn("sse: replay", "sala", s.ID, "err", err)
	}
	for _, sub := range previos {
		if sub.Tipo == "final" || sub.Tipo == "parcial" {
			if !enviar(sub.Tipo, sub.ID, sub) {
				return
			}
		}
		ultimo = sub.ID
	}

	latido := time.NewTicker(a.hub.cfg.Latido)
	defer latido.Stop()
	// El nivel de la voz, para la onda de la sala: cada 500 ms y sólo si
	// cambió más de 2 dB (una sala callada no manda nada). Son bytes: el
	// costo es del orden de un latido, no de un subtítulo.
	onda := time.NewTicker(500 * time.Millisecond)
	defer onda.Stop()
	ultimoNivel := -999.0
	for {
		select {
		case <-ctx.Done():
			return
		case <-onda.C:
			n := a.hub.Resumen(s.ID).Nivel
			if n-ultimoNivel > 2 || ultimoNivel-n > 2 {
				ultimoNivel = n
				if !enviar("nivel", "", map[string]int{"db": int(n)}) {
					return
				}
			}
		case m, abierto := <-c:
			if !abierto {
				return // lento: se lo soltó; el cliente se reconecta
			}
			if m.Sub == nil {
				if !enviar(m.Evento, "", m.Datos) {
					return
				}
				continue
			}
			sub := *m.Sub
			if !idMayor(sub.ID, ultimo) {
				continue
			}
			ultimo = sub.ID
			if sub.Tipo != "final" && sub.Tipo != "parcial" {
				continue
			}
			// Los parciales no llevan id: si se cortó en medio de uno,
			// al volver no hay que "recuperarlo", ya lo pisó el final.
			id := sub.ID
			if sub.Tipo == "parcial" {
				id = ""
			}
			if !enviar(sub.Tipo, id, sub) {
				return
			}
		case <-latido.C:
			if !enviar("estado", "", a.eventoEstado(ctx, s)) {
				return
			}
		}
	}
}

// ---- panel: sesión ----------------------------------------------------------------------

type credenciales struct {
	Usuario  string `json:"usuario"`
	Password string `json:"password"`
}

func (a *API) entrar(w http.ResponseWriter, r *http.Request) {
	var c credenciales
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&c); err != nil {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "se esperaba {usuario, password}")
		return
	}
	cfg := a.hub.cfg
	if cfg.OperadorUsuario == "" || cfg.OperadorPassword == "" {
		jsonError(w, http.StatusServiceUnavailable, "sin_operador", "el panel no tiene operador configurado")
		return
	}
	// Las dos comparaciones SIEMPRE, y en tiempo constante: que no se
	// pueda saber por cuánto tarda si el usuario existía.
	u := igualConstante(c.Usuario, cfg.OperadorUsuario)
	p := igualConstante(c.Password, cfg.OperadorPassword)
	if !u || !p {
		time.Sleep(400 * time.Millisecond) // frena el adivinar a mano
		jsonError(w, http.StatusUnauthorized, "credenciales", "usuario o clave incorrectos")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieOperador, Value: a.firma.Sesion(a.hub.ahora()),
		Path: "/api", HttpOnly: true, Secure: cfg.CookieSegura, SameSite: http.SameSiteStrictMode,
		MaxAge: int(duracionSesion.Seconds())})
	jsonOK(w, map[string]bool{"ok": true})
}

func (a *API) salir(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieOperador, Value: "", Path: "/api", MaxAge: -1,
		HttpOnly: true, Secure: a.hub.cfg.CookieSegura, SameSite: http.SameSiteStrictMode})
	jsonOK(w, map[string]bool{"ok": true})
}

// ---- panel: salas ------------------------------------------------------------------------

type pedidoSala struct {
	Nombre   string  `json:"nombre"`
	Idioma   string  `json:"idioma"`
	Backend  string  `json:"backend"`
	Glosario *string `json:"glosario"` // nil = no tocar; "" = vaciar
}

// normalizarGlosario limpia y acota: hasta 20 términos de hasta 40
// caracteres. Whisper recibe esto en cada tramo; un glosario enorme es
// más tokens de prompt por llamada y más ocasiones de meter un término
// donde no va.
func normalizarGlosario(g string) (string, bool) {
	var terminos []string
	for _, t := range strings.Split(g, ",") {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if len(t) > 40 || len(terminos) == 20 {
			return "", false
		}
		terminos = append(terminos, t)
	}
	return strings.Join(terminos, ", "), true
}

func nuevoID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "sala-" + hex.EncodeToString(b)
}

func (a *API) crearSala(w http.ResponseWriter, r *http.Request) {
	var p pedidoSala
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p); err != nil {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "se esperaba {nombre, idioma, backend}")
		return
	}
	p.Nombre = strings.TrimSpace(p.Nombre)
	if p.Nombre == "" || len(p.Nombre) > 80 {
		jsonError(w, http.StatusBadRequest, "nombre", "el nombre va de 1 a 80 caracteres")
		return
	}
	if !idiomaValido[p.Idioma] {
		jsonError(w, http.StatusBadRequest, "idioma", "idioma de la charla: en o es")
		return
	}
	if p.Backend == "" {
		p.Backend = a.hub.cfg.BackendDefecto
	}
	if !backendValido[p.Backend] {
		jsonError(w, http.StatusBadRequest, "backend", "backend: gpu, cpu o gemini")
		return
	}
	s := Sala{ID: nuevoID(), Nombre: p.Nombre, Idioma: p.Idioma, Backend: p.Backend, Creada: a.hub.ahora().Unix()}
	if err := a.hub.bus.GuardarSala(r.Context(), s); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"sala": s, "token": a.firma.TokenEmision(s.ID)})
}

func (a *API) editarSala(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoSala
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p); err != nil {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "JSON inválido")
		return
	}
	if p.Backend != "" {
		if !backendValido[p.Backend] {
			jsonError(w, http.StatusBadRequest, "backend", "backend: gpu, cpu o gemini")
			return
		}
		s.Backend = p.Backend
	}
	if p.Idioma != "" {
		if !idiomaValido[p.Idioma] {
			jsonError(w, http.StatusBadRequest, "idioma", "idioma de la charla: en o es")
			return
		}
		s.Idioma = p.Idioma
	}
	if n := strings.TrimSpace(p.Nombre); n != "" && len(n) <= 80 {
		s.Nombre = n
	}
	if p.Glosario != nil {
		g, ok := normalizarGlosario(*p.Glosario)
		if !ok {
			jsonError(w, http.StatusBadRequest, "glosario", "hasta 20 términos separados por comas, de hasta 40 caracteres")
			return
		}
		s.Glosario = g
	}
	if err := a.hub.bus.GuardarSala(r.Context(), s); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	jsonOK(w, s)
}

func (a *API) borrarSala(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	if s.Demo {
		jsonError(w, http.StatusConflict, "sala_demo", "las salas demo se apagan con SALAS_DEMO")
		return
	}
	if err := a.hub.bus.BorrarSala(r.Context(), s.ID); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (a *API) tokenSala(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	jsonOK(w, map[string]string{"token": a.firma.TokenEmision(s.ID)})
}

// ---- panel: estado ---------------------------------------------------------------------------

type filaEstado struct {
	SalaPublica
	Vivo        ResumenVivo `json:"vivo"`
	Interaccion Interaccion `json:"interaccion"`
}

func (a *API) estado(w http.ResponseWriter, r *http.Request) {
	salas, err := a.hub.bus.Salas(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	pub := make([]SalaPublica, 0, len(salas))
	for _, s := range salas {
		pub = append(pub, a.publica(s))
	}
	ordenarSalas(pub)
	filas := make([]filaEstado, 0, len(pub))
	for _, p := range pub {
		filas = append(filas, filaEstado{SalaPublica: p, Vivo: a.hub.Resumen(p.ID),
			Interaccion: a.interaccion(r.Context(), p.ID)})
	}
	m, err := a.hub.bus.Motor(r.Context(), a.hub.ahora())
	motor := any(m)
	if err != nil {
		motor = map[string]string{"error": "no pude leer el motor"}
	}
	jsonOK(w, map[string]any{"motor": motor, "salas": filas, "ahora": a.hub.ahora().UnixMilli(),
		"max_salas": a.hub.cfg.MaxSalas})
}
