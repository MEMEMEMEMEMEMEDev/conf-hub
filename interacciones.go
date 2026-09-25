package main

// Lo que la audiencia HACE en una sala, además de leer:
//
//   reacciones      aplauso, fuego, duda. Sin texto. Se agregan por sala y
//                   se reparten UNA vez por segundo: cien personas
//                   aplaudiendo son un evento, no cien.
//   no se entiende  la señal de accesibilidad: el subtítulo va atrasado o
//                   mal. NO se reparte a la sala (alarmaría); la ve
//                   producción en el panel, como cuenta de los últimos 2 min.
//   preguntas       texto libre → nace PENDIENTE y sólo la ve quien la
//                   escribió y producción. Publicada, la sala la vota.
//   glosario        la audiencia sugiere un nombre mal transcrito;
//                   producción lo aprueba y entra al glosario de la sala.
//
// Todo lo que entra de la audiencia pasa por un tope por visitante (además
// del que la plataforma pone en el borde): la URL es pública.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ---- quién es el visitante ---------------------------------------------------

const cookieVisitante = "conf_v"

// visitante devuelve un id anónimo estable del navegador (cookie), y lo
// crea si no hay. No identifica a nadie: sirve para que un voto no se
// cuente dos veces y para que quien escribió una pregunta la vea pendiente.
func (a *API) visitante(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookieVisitante); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	b := make([]byte, 16)
	rand.Read(b)
	v := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: cookieVisitante, Value: v, Path: "/api", HttpOnly: true,
		Secure: a.hub.cfg.CookieSegura, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
	return v
}

// ipDe: detrás de Cloudflare, la IP real viene en CF-Connecting-IP. Se le
// cree sólo si CONFIAR_CF=1: sin el túnel delante, cualquiera la inventa.
func (a *API) ipDe(r *http.Request) string {
	if a.hub.cfg.ConfiarCF {
		if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- el tope por visitante ----------------------------------------------------

type balde struct {
	fichas float64
	ultima time.Time
}

type Limitador struct {
	mu     sync.Mutex
	baldes map[string]*balde
}

func NuevoLimitador() *Limitador { return &Limitador{baldes: map[string]*balde{}} }

// Tomar descuenta una ficha del balde `clave` (capacidad `rafaga`, se
// rellena a `porSeg`). false = esperá.
func (l *Limitador) Tomar(clave string, rafaga, porSeg float64, ahora time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.baldes[clave]
	if !ok {
		b = &balde{fichas: rafaga, ultima: ahora}
		l.baldes[clave] = b
	}
	b.fichas = min(rafaga, b.fichas+ahora.Sub(b.ultima).Seconds()*porSeg)
	b.ultima = ahora
	if b.fichas < 1 {
		return false
	}
	b.fichas--
	// Limpieza perezosa: que el mapa no crezca con cada IP que pasó una vez.
	if len(l.baldes) > 20000 {
		for k, x := range l.baldes {
			if ahora.Sub(x.ultima) > 10*time.Minute {
				delete(l.baldes, k)
			}
		}
	}
	return true
}

// permitido aplica el tope a la IP Y al visitante: cambiar de cookie no
// alcanza para saltarlo, y una red compartida (el wifi del evento) no se
// frena entera por uno.
func (a *API) permitido(w http.ResponseWriter, r *http.Request, accion string, rafaga, porSeg float64) bool {
	ahora := a.hub.ahora()
	v := a.visitante(w, r)
	ip := a.ipDe(r)
	// La IP tiene 20× de holgura: detrás de ella puede haber una sala entera.
	if !a.limite.Tomar(accion+"|v|"+v, rafaga, porSeg, ahora) ||
		!a.limite.Tomar(accion+"|ip|"+ip, rafaga*20, porSeg*20, ahora) {
		w.Header().Set("Retry-After", "5")
		jsonError(w, http.StatusTooManyRequests, "despacio", "demasiado seguido: probá en unos segundos")
		return false
	}
	return true
}

// ---- reacciones -------------------------------------------------------------------

var reaccionesValidas = map[string]bool{"aplauso": true, "fuego": true, "duda": true, "noseentiende": true}

type Reacciones struct {
	mu     sync.Mutex
	cuenta map[string]map[string]int // sala → reacción → cuántas en este segundo
}

func (h *Hub) sumarReaccion(sala, id string) {
	if id == "noseentiende" {
		v := h.vivo(sala)
		v.mu.Lock()
		v.NoSeEntiende = append(v.NoSeEntiende, h.ahora())
		v.mu.Unlock()
		return
	}
	h.reacciones.mu.Lock()
	defer h.reacciones.mu.Unlock()
	if h.reacciones.cuenta[sala] == nil {
		h.reacciones.cuenta[sala] = map[string]int{}
	}
	h.reacciones.cuenta[sala][id]++
}

// RepartirReacciones corre cada segundo: lo juntado va a cada sala como un
// evento `reacciones` {aplauso: 3, fuego: 1}.
func (h *Hub) RepartirReacciones(ctx context.Context, cada time.Duration) {
	t := time.NewTicker(cada)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.reacciones.mu.Lock()
		lote := h.reacciones.cuenta
		h.reacciones.cuenta = map[string]map[string]int{}
		h.reacciones.mu.Unlock()
		for sala, c := range lote {
			h.difusor(sala).Emitir("reacciones", c)
		}
	}
}

type pedidoReaccion struct {
	ID string `json:"id"`
}

func (a *API) reaccionar(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoReaccion
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&p) != nil || !reaccionesValidas[p.ID] {
		jsonError(w, http.StatusBadRequest, "reaccion", "reacción: aplauso, fuego, duda o noseentiende")
		return
	}
	// Ráfaga de 6, una cada 0,7 s: se puede aplaudir con ganas, no
	// sostener un bot.
	if !a.permitido(w, r, "reaccion", 6, 1.4) {
		return
	}
	a.hub.sumarReaccion(s.ID, p.ID)
	w.WriteHeader(http.StatusNoContent)
}

// ---- preguntas --------------------------------------------------------------------

const (
	maxPregunta         = 280
	maxPreguntasPorSala = 300
)

func clavePreguntas(sala string) string   { return "conf:preguntas:" + sala }
func claveVotos(sala, id string) string   { return "conf:votos:" + sala + ":" + id }
func claveSugerencias(sala string) string { return "conf:sugerencias:" + sala }

type Pregunta struct {
	ID     string `json:"id"`
	Texto  string `json:"texto"`
	Estado string `json:"estado"` // pendiente | publicada | respondida | descartada
	Creada int64  `json:"creada"`
	Autor  string `json:"-"` // hash del visitante: nunca sale por la API
	Votos  int64  `json:"votos"`
	Votada bool   `json:"votada"`
	Mia    bool   `json:"mia,omitempty"`
}

type preguntaGuardada struct {
	ID     string `json:"id"`
	Texto  string `json:"texto"`
	Estado string `json:"estado"`
	Creada int64  `json:"creada"`
	Autor  string `json:"autor"`
}

func hashVisitante(v string) string {
	h := sha256.Sum256([]byte("conf-visitante:" + v))
	return hex.EncodeToString(h[:8])
}

// limpiarTexto: sin caracteres de control, espacios colapsados, recortado.
func limpiarTexto(t string) string {
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t)
	return strings.Join(strings.Fields(t), " ")
}

func (b *Bus) guardarPregunta(ctx context.Context, sala string, p preguntaGuardada) error {
	j, _ := json.Marshal(p)
	return b.r.HSet(ctx, clavePreguntas(sala), p.ID, j).Err()
}

func (b *Bus) preguntas(ctx context.Context, sala, visitante string) ([]Pregunta, error) {
	m, err := b.r.HGetAll(ctx, clavePreguntas(sala)).Result()
	if err != nil {
		return nil, err
	}
	yo := ""
	if visitante != "" {
		yo = hashVisitante(visitante)
	}
	out := make([]Pregunta, 0, len(m))
	for _, j := range m {
		var g preguntaGuardada
		if json.Unmarshal([]byte(j), &g) != nil {
			continue
		}
		p := Pregunta{ID: g.ID, Texto: g.Texto, Estado: g.Estado, Creada: g.Creada, Autor: g.Autor, Mia: yo != "" && g.Autor == yo}
		p.Votos, _ = b.r.SCard(ctx, claveVotos(sala, g.ID)).Result()
		if visitante != "" {
			p.Votada, _ = b.r.SIsMember(ctx, claveVotos(sala, g.ID), visitante).Result()
		}
		out = append(out, p)
	}
	// Las publicadas por votos, después las respondidas; a igual votos, la
	// más vieja primero (llegó antes).
	orden := map[string]int{"publicada": 0, "pendiente": 1, "respondida": 2, "descartada": 3}
	slices.SortFunc(out, func(a, b Pregunta) int {
		if orden[a.Estado] != orden[b.Estado] {
			return orden[a.Estado] - orden[b.Estado]
		}
		if a.Votos != b.Votos {
			return int(b.Votos - a.Votos)
		}
		return int(a.Creada - b.Creada)
	})
	return out, nil
}

// publicas: lo que ve la sala entera (sin pendientes ni descartadas).
func publicas(ps []Pregunta) []Pregunta {
	out := []Pregunta{}
	for _, p := range ps {
		if p.Estado == "publicada" || p.Estado == "respondida" {
			p.Votada, p.Mia = false, false // el evento es para todos: sin datos de nadie
			out = append(out, p)
		}
	}
	return out
}

// avisarPreguntas manda a la sala la lista pública nueva.
func (a *API) avisarPreguntas(ctx context.Context, sala string) {
	ps, err := a.hub.bus.preguntas(ctx, sala, "")
	if err == nil {
		a.hub.difusor(sala).Emitir("preguntas", publicas(ps))
	}
}

// GET /api/salas/{id}/preguntas — la lista pública + las pendientes propias.
// Con sesión de operador y ?todas=1, todas menos las descartadas.
func (a *API) listarPreguntas(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	v := a.visitante(w, r)
	ps, err := a.hub.bus.preguntas(r.Context(), s.ID, v)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	todas := r.URL.Query().Get("todas") == "1" && a.esOperador(r)
	out := []Pregunta{}
	for _, p := range ps {
		switch {
		case p.Estado == "descartada":
		case p.Estado == "pendiente" && !(todas || p.Mia):
		default:
			out = append(out, p)
		}
	}
	jsonOK(w, out)
}

type pedidoPregunta struct {
	Texto string `json:"texto"`
}

func (a *API) preguntar(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoPregunta
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p) != nil {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "se esperaba {texto}")
		return
	}
	texto := limpiarTexto(p.Texto)
	if len([]rune(texto)) < 5 || len([]rune(texto)) > maxPregunta {
		jsonError(w, http.StatusBadRequest, "texto", "la pregunta va de 5 a 280 caracteres")
		return
	}
	// Una pregunta cada 30 s por visitante, ráfaga de 2.
	if !a.permitido(w, r, "pregunta", 2, 1.0/30) {
		return
	}
	n, _ := a.hub.bus.r.HLen(r.Context(), clavePreguntas(s.ID)).Result()
	if n >= maxPreguntasPorSala {
		jsonError(w, http.StatusConflict, "lleno", "esta sala ya no recibe más preguntas")
		return
	}
	id := make([]byte, 5)
	rand.Read(id)
	g := preguntaGuardada{ID: hex.EncodeToString(id), Texto: texto, Estado: "pendiente",
		Creada: a.hub.ahora().UnixMilli(), Autor: hashVisitante(a.visitante(w, r))}
	if err := a.hub.bus.guardarPregunta(r.Context(), s.ID, g); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, Pregunta{ID: g.ID, Texto: g.Texto, Estado: g.Estado, Creada: g.Creada, Mia: true})
}

func (b *Bus) pregunta(ctx context.Context, sala, id string) (preguntaGuardada, error) {
	var g preguntaGuardada
	j, err := b.r.HGet(ctx, clavePreguntas(sala), id).Result()
	if err != nil {
		return g, ErrNoExiste
	}
	return g, json.Unmarshal([]byte(j), &g)
}

// POST /api/salas/{id}/preguntas/{pid}/voto — alterna el voto propio.
func (a *API) votar(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	g, err := a.hub.bus.pregunta(r.Context(), s.ID, pid)
	if err != nil || g.Estado != "publicada" {
		jsonError(w, http.StatusNotFound, "sin_pregunta", "esa pregunta no se puede votar")
		return
	}
	if !a.permitido(w, r, "voto", 5, 1) {
		return
	}
	v := a.visitante(w, r)
	k := claveVotos(s.ID, pid)
	esta, _ := a.hub.bus.r.SIsMember(r.Context(), k, v).Result()
	if esta {
		a.hub.bus.r.SRem(r.Context(), k, v)
	} else {
		a.hub.bus.r.SAdd(r.Context(), k, v)
	}
	votos, _ := a.hub.bus.r.SCard(r.Context(), k).Result()
	a.avisarPreguntas(r.Context(), s.ID)
	jsonOK(w, map[string]any{"votos": votos, "votada": !esta})
}

type pedidoModerar struct {
	Estado string `json:"estado"`
}

// PATCH /api/salas/{id}/preguntas/{pid} (panel) — publicar, responder, descartar.
func (a *API) moderar(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoModerar
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&p)
	if p.Estado != "publicada" && p.Estado != "respondida" && p.Estado != "descartada" {
		jsonError(w, http.StatusBadRequest, "estado", "estado: publicada, respondida o descartada")
		return
	}
	g, err := a.hub.bus.pregunta(r.Context(), s.ID, r.PathValue("pid"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "sin_pregunta", "no existe")
		return
	}
	g.Estado = p.Estado
	if err := a.hub.bus.guardarPregunta(r.Context(), s.ID, g); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	a.avisarPreguntas(r.Context(), s.ID)
	jsonOK(w, map[string]string{"estado": g.Estado})
}

// ---- sugerencias al glosario ----------------------------------------------------------

type Sugerencia struct {
	Termino string `json:"termino"`
	Veces   int    `json:"veces"`
	Creada  int64  `json:"creada"`
}

type pedidoSugerencia struct {
	Termino string `json:"termino"`
}

func (a *API) sugerir(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoSugerencia
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&p) != nil {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "se esperaba {termino}")
		return
	}
	t := limpiarTexto(p.Termino)
	if n := len([]rune(t)); n < 2 || n > 40 || strings.Contains(t, ",") {
		jsonError(w, http.StatusBadRequest, "termino", "un término de 2 a 40 caracteres, sin comas")
		return
	}
	if !a.permitido(w, r, "sugerencia", 3, 1.0/20) {
		return
	}
	ctx := r.Context()
	k := claveSugerencias(s.ID)
	clave := strings.ToLower(t)
	var sg Sugerencia
	if j, err := a.hub.bus.r.HGet(ctx, k, clave).Result(); err == nil {
		json.Unmarshal([]byte(j), &sg)
	} else if n, _ := a.hub.bus.r.HLen(ctx, k).Result(); n >= 100 {
		jsonError(w, http.StatusConflict, "lleno", "esta sala ya tiene muchas sugerencias pendientes")
		return
	} else {
		sg = Sugerencia{Termino: t, Creada: a.hub.ahora().UnixMilli()}
	}
	sg.Veces++
	j, _ := json.Marshal(sg)
	a.hub.bus.r.HSet(ctx, k, clave, j)
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]bool{"ok": true})
}

func (b *Bus) sugerencias(ctx context.Context, sala string) ([]Sugerencia, error) {
	m, err := b.r.HGetAll(ctx, claveSugerencias(sala)).Result()
	if err != nil {
		return nil, err
	}
	out := []Sugerencia{}
	for _, j := range m {
		var s Sugerencia
		if json.Unmarshal([]byte(j), &s) == nil {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Sugerencia) int {
		if a.Veces != b.Veces {
			return b.Veces - a.Veces
		}
		return int(a.Creada - b.Creada)
	})
	return out, nil
}

func (a *API) listarSugerencias(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	sg, err := a.hub.bus.sugerencias(r.Context(), s.ID)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	jsonOK(w, sg)
}

type pedidoResolver struct {
	Termino string `json:"termino"`
	Aprobar bool   `json:"aprobar"`
}

// POST /api/salas/{id}/sugerencias/resolver (panel): aprobar = entra al
// glosario de la sala; si no, se descarta. Las dos la sacan de la lista.
func (a *API) resolverSugerencia(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	var p pedidoResolver
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&p) != nil || p.Termino == "" {
		jsonError(w, http.StatusBadRequest, "entrada_invalida", "se esperaba {termino, aprobar}")
		return
	}
	ctx := r.Context()
	clave := strings.ToLower(limpiarTexto(p.Termino))
	j, err := a.hub.bus.r.HGet(ctx, claveSugerencias(s.ID), clave).Result()
	if err != nil {
		jsonError(w, http.StatusNotFound, "sin_sugerencia", "no existe")
		return
	}
	var sg Sugerencia
	json.Unmarshal([]byte(j), &sg)
	if p.Aprobar {
		nuevo := sg.Termino
		if s.Glosario != "" {
			nuevo = s.Glosario + ", " + sg.Termino
		}
		g, ok := normalizarGlosario(nuevo)
		if !ok {
			jsonError(w, http.StatusConflict, "glosario_lleno", "el glosario ya tiene 20 términos: sacá alguno antes")
			return
		}
		s.Glosario = g
		if err := a.hub.bus.GuardarSala(ctx, s); err != nil {
			jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
			return
		}
	}
	a.hub.bus.r.HDel(ctx, claveSugerencias(s.ID), clave)
	jsonOK(w, map[string]string{"glosario": s.Glosario})
}

// ---- lo que el panel necesita contar ---------------------------------------------------

type Interaccion struct {
	NoSeEntiende int `json:"no_se_entiende_2min"`
	Pendientes   int `json:"preguntas_pendientes"`
	Sugerencias  int `json:"sugerencias"`
}

func (a *API) interaccion(ctx context.Context, sala string) Interaccion {
	var in Interaccion
	v := a.hub.vivo(sala)
	corte := a.hub.ahora().Add(-2 * time.Minute)
	v.mu.Lock()
	i := 0
	for i < len(v.NoSeEntiende) && v.NoSeEntiende[i].Before(corte) {
		i++
	}
	v.NoSeEntiende = v.NoSeEntiende[i:]
	in.NoSeEntiende = len(v.NoSeEntiende)
	v.mu.Unlock()
	if ps, err := a.hub.bus.preguntas(ctx, sala, ""); err == nil {
		for _, p := range ps {
			if p.Estado == "pendiente" {
				in.Pendientes++
			}
		}
	}
	if n, err := a.hub.bus.r.HLen(ctx, claveSugerencias(sala)).Result(); err == nil {
		in.Sugerencias = int(n)
	}
	return in
}

// ---- la bandeja de producción ---------------------------------------------------------

type ItemBandeja struct {
	Sala       string    `json:"sala"`
	NombreSala string    `json:"nombre_sala"`
	Pregunta   *Pregunta `json:"pregunta,omitempty"`
}

type SugerenciaSala struct {
	Sugerencia
	Sala       string `json:"sala"`
	NombreSala string `json:"nombre_sala"`
}

// GET /api/bandeja (panel): lo que espera una decisión de producción en
// TODAS las salas, en un solo lugar. Con diez escenarios, abrir la
// moderación sala por sala es perderse preguntas.
func (a *API) bandeja(w http.ResponseWriter, r *http.Request) {
	salas, err := a.hub.bus.Salas(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	preguntas := []ItemBandeja{}
	sugerencias := []SugerenciaSala{}
	for _, s := range salas {
		ps, err := a.hub.bus.preguntas(r.Context(), s.ID, "")
		if err == nil {
			for _, p := range ps {
				if p.Estado == "pendiente" {
					preguntas = append(preguntas, ItemBandeja{Sala: s.ID, NombreSala: s.Nombre, Pregunta: &p})
				}
			}
		}
		sg, err := a.hub.bus.sugerencias(r.Context(), s.ID)
		if err == nil {
			for _, x := range sg {
				sugerencias = append(sugerencias, SugerenciaSala{Sugerencia: x, Sala: s.ID, NombreSala: s.Nombre})
			}
		}
	}
	// La pendiente más vieja primero: es la que más lleva esperando.
	slices.SortFunc(preguntas, func(x, y ItemBandeja) int { return int(x.Pregunta.Creada - y.Pregunta.Creada) })
	jsonOK(w, map[string]any{"preguntas": preguntas, "sugerencias": sugerencias})
}
