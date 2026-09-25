package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
)

// ---- audio sintético ------------------------------------------------------

// ruido de sala: ~ -60 dBFS
func ruido(r *rand.Rand, seg float64) []int16 {
	n := int(seg * FrecMuestreo)
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(r.NormFloat64() * 30)
	}
	return out
}

// "habla": un tono modulado a 4 Hz (sílabas) a ~ -12 dBFS, sobre el ruido.
func habla(r *rand.Rand, seg float64) []int16 {
	out := ruido(r, seg)
	for i := range out {
		t := float64(i) / FrecMuestreo
		env := 0.55 + 0.45*math.Sin(2*math.Pi*4*t)
		out[i] += int16(8000 * env * math.Sin(2*math.Pi*220*t))
	}
	return out
}

func concat(partes ...[]int16) []int16 {
	var out []int16
	for _, p := range partes {
		out = append(out, p...)
	}
	return out
}

func segmentar(t *testing.T, cfg ConfigSegmentador, pcm []int16) []Tramo {
	t.Helper()
	var tramos []Tramo
	s := NuevoSegmentador(cfg, time.Unix(1_700_000_000, 0), func(tr Tramo) { tramos = append(tramos, tr) })
	// En trozos de 100 ms, como llegan por el WebSocket.
	for i := 0; i < len(pcm); i += 1600 {
		s.Escribir(pcm[i:min(i+1600, len(pcm))])
	}
	return tramos
}

func durTramo(tr Tramo) time.Duration { return tr.Fin.Sub(tr.Inicio) }

// ---- segmentador ------------------------------------------------------------

func TestSilencioNoProduceTramos(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	if tr := segmentar(t, ConfigPorDefecto(), ruido(r, 30)); len(tr) != 0 {
		t.Fatalf("30 s de ruido de sala dieron %d tramos: Whisper alucinaría sobre ellos", len(tr))
	}
}

func TestCierraEnLaPausaDespuesDelMinimo(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	// 9 s de habla, 0,6 s de pausa, 9 s de habla, silencio final.
	pcm := concat(ruido(r, 1), habla(r, 9), ruido(r, 0.6), habla(r, 9), ruido(r, 2))
	tr := segmentar(t, ConfigPorDefecto(), pcm)
	if len(tr) != 2 {
		t.Fatalf("esperaba 2 tramos (uno por frase), hubo %d", len(tr))
	}
	for i, x := range tr {
		d := durTramo(x)
		if d < 8*time.Second || d > 12*time.Second {
			t.Errorf("tramo %d dura %s, fuera de [8 s, 12 s]", i, d)
		}
		if base := time.Unix(1_700_000_000, 0).Unix(); !x.Final || x.Seq != base+int64(i+1) {
			t.Errorf("tramo %d: final=%v seq=%d", i, x.Final, x.Seq)
		}
	}
	// El primero tiene que terminar en la pausa: ~1 s + 9 s + la pausa.
	if fin := tr[0].Fin.Sub(time.Unix(1_700_000_000, 0)); fin < 10*time.Second || fin > 10700*time.Millisecond {
		t.Errorf("el primer tramo termina a %s del origen; la pausa estaba en 10–10,6 s", fin)
	}
}

func TestNoCierraAntesDelMinimoEnUnaPausaCorta(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	// Pausas de 400 ms cada 3 s: con tramos de 4 s se partiría; acá no.
	var partes [][]int16
	for i := 0; i < 5; i++ {
		partes = append(partes, habla(r, 3), ruido(r, 0.4))
	}
	partes = append(partes, ruido(r, 2))
	tr := segmentar(t, ConfigPorDefecto(), concat(partes...))
	// El último puede ser corto: el orador terminó. Los demás, no.
	for i, x := range tr[:len(tr)-1] {
		if durTramo(x) < 8*time.Second {
			t.Errorf("tramo %d de %s: cerró en una pausa antes del mínimo", i, durTramo(x))
		}
	}
}

func TestHablaSinPausaSeCortaAlMaximoEnElFrameMasSilencioso(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	// 11 s de habla, 150 ms de casi-silencio (menos que una pausa), habla.
	pcm := concat(habla(r, 11), ruido(r, 0.15), habla(r, 14), ruido(r, 2))
	tr := segmentar(t, ConfigPorDefecto(), pcm)
	if len(tr) < 2 {
		t.Fatalf("esperaba al menos 2 tramos, hubo %d", len(tr))
	}
	d := durTramo(tr[0])
	if d > 12*time.Second {
		t.Fatalf("el primer tramo dura %s: superó el máximo", d)
	}
	// El corte tiene que caer en el hueco de 11,0–11,15 s, no en medio
	// de una "palabra" a los 12 s.
	if d < 11*time.Second || d > 11200*time.Millisecond {
		t.Errorf("el corte forzado cayó a los %s; el hueco estaba en 11,0–11,15 s", d)
	}
	// Y no se pierde audio: el segundo empieza donde terminó el primero.
	if !tr[1].Inicio.Equal(tr[0].Fin) {
		t.Errorf("hueco entre tramos: %s → %s", tr[0].Fin, tr[1].Inicio)
	}
}

func TestParcialesSaleConLaVentanaAbierta(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	cfg := ConfigPorDefecto()
	cfg.CadaParcial = 3 * time.Second
	tr := segmentar(t, cfg, concat(habla(r, 9), ruido(r, 2)))
	var parciales, finales int
	for _, x := range tr {
		if x.Final {
			finales++
		} else {
			parciales++
			if x.Seq != 1_700_000_000+1 {
				t.Errorf("el parcial debe anunciar el seq del final que viene, trae %d", x.Seq)
			}
		}
	}
	if finales != 1 || parciales < 2 {
		t.Fatalf("finales=%d parciales=%d; esperaba 1 final y ≥2 parciales", finales, parciales)
	}
}

// ---- API sobre un redis en memoria ------------------------------------------

type entornoPrueba struct {
	mr  *miniredis.Miniredis
	bus *Bus
	hub *Hub
	api *API
	srv *httptest.Server
	ctx context.Context
}

func nuevoEntorno(t *testing.T) *entornoPrueba {
	t.Helper()
	mr := miniredis.RunT(t)
	bus, err := NuevoBus("redis://" + mr.Addr() + "/0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := CargarConfig()
	cfg.ClaveFirma = "clave-de-prueba-larga"
	cfg.OperadorUsuario, cfg.OperadorPassword = "op", "secreta"
	cfg.CookieSegura = false
	cfg.SalasDemo = 0
	cfg.Latido = 200 * time.Millisecond
	hub := NuevoHub(bus, cfg)
	api := NuevaAPI(hub)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Arrancar(ctx)
	go hub.RepartirReacciones(ctx, 50*time.Millisecond)
	for {
		hub.mu.Lock()
		listo := hub.raiz != nil
		hub.mu.Unlock()
		if listo {
			break
		}
		time.Sleep(time.Millisecond)
	}
	srv := httptest.NewServer(api.Rutas())
	t.Cleanup(srv.Close)
	return &entornoPrueba{mr, bus, hub, api, srv, ctx}
}

func (e *entornoPrueba) sala(t *testing.T, id, idioma string) Sala {
	s := Sala{ID: id, Nombre: "Sala " + id, Idioma: idioma, Backend: "gpu", Creada: 1}
	if err := e.bus.GuardarSala(e.ctx, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *entornoPrueba) sub(t *testing.T, sala, orig string) string {
	id, err := e.bus.PublicarSubtitulo(e.ctx, sala, Subtitulo{Tipo: "final", Orig: orig, Idioma: "en",
		Es: "es:" + orig, TIni: 1000, TFin: 9000, Backend: "gpu", LatMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type eventoSSE struct{ id, evento, datos string }

// leerSSE lee eventos hasta tener n que no sean "estado", o vencer.
func leerSSE(t *testing.T, url, ultimo string, n int) []eventoSSE {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if ultimo != "" {
		req.Header.Set("Last-Event-ID", ultimo)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-transform") {
		t.Fatalf("Cache-Control %q: el borde podría bufferizar el SSE", cc)
	}
	var out []eventoSSE
	var ev eventoSSE
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "id: "):
			ev.id = l[4:]
		case strings.HasPrefix(l, "event: "):
			ev.evento = l[7:]
		case strings.HasPrefix(l, "data: "):
			ev.datos = l[6:]
		case l == "":
			if ev.evento == "final" || ev.evento == "parcial" {
				out = append(out, ev)
				if len(out) == n {
					return out
				}
			}
			ev = eventoSSE{}
		}
	}
	t.Fatalf("el SSE terminó con %d de %d eventos", len(out), n)
	return nil
}

func TestSSEReconexionConLastEventIDNoPierdeNiRepite(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "s1", "en")
	id1 := e.sub(t, "s1", "uno")
	e.sub(t, "s1", "dos")
	e.sub(t, "s1", "tres")
	url := e.srv.URL + "/api/salas/s1/subtitulos"

	// Reconexión después de "uno": tiene que llegar dos y tres, en orden.
	ev := leerSSE(t, url, id1, 2)
	var a, b Subtitulo
	json.Unmarshal([]byte(ev[0].datos), &a)
	json.Unmarshal([]byte(ev[1].datos), &b)
	if a.Orig != "dos" || b.Orig != "tres" {
		t.Fatalf("replay trajo %q, %q; esperaba dos, tres", a.Orig, b.Orig)
	}
	if ev[0].id == "" || ev[0].evento != "final" {
		t.Fatalf("un final sin id no se puede retomar: %+v", ev[0])
	}
}

func TestSSEEntregaLoNuevoEnVivo(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "s2", "en")
	url := e.srv.URL + "/api/salas/s2/subtitulos"
	go func() {
		// Esperar a que el espectador esté suscrito.
		for e.hub.difusor("s2").Espectadores() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		e.sub(t, "s2", "en vivo")
	}()
	ev := leerSSE(t, url, "", 1)
	if !strings.Contains(ev[0].datos, `"orig":"en vivo"`) || !strings.Contains(ev[0].datos, `"es":"es:en vivo"`) {
		t.Fatalf("evento: %+v", ev[0])
	}
	// El panel dice qué backend ATENDIÓ, no el que la sala pide.
	deadline := time.Now().Add(2 * time.Second)
	for e.hub.Resumen("s2").Atendio == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a := e.hub.Resumen("s2").Atendio; a != "gpu" {
		t.Fatalf("atendio=%q, esperaba el backend del último final (gpu)", a)
	}
}

func TestSalaInexistente404(t *testing.T) {
	e := nuevoEntorno(t)
	for _, ruta := range []string{"/api/salas/nada/subtitulos", "/api/salas/nada", "/api/salas/..%2f/subtitulos"} {
		res, err := http.Get(e.srv.URL + ruta)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("%s → %d, esperaba 404", ruta, res.StatusCode)
		}
	}
}

// ---- permisos: se prueban en la API, no escondiendo un botón ----------------

func TestPanelSinSesionEs401YConSesionAnda(t *testing.T) {
	e := nuevoEntorno(t)
	cuerpo := `{"nombre":"Auditorio","idioma":"en","backend":"gpu"}`
	res, _ := http.Post(e.srv.URL+"/api/salas", "application/json", strings.NewReader(cuerpo))
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("crear sala sin sesión → %d, esperaba 401", res.StatusCode)
	}
	res, _ = http.Get(e.srv.URL + "/api/estado")
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("estado sin sesión → %d, esperaba 401", res.StatusCode)
	}

	res, _ = http.Post(e.srv.URL+"/api/sesion", "application/json", strings.NewReader(`{"usuario":"op","password":"mal"}`))
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("clave incorrecta → %d", res.StatusCode)
	}

	res, _ = http.Post(e.srv.URL+"/api/sesion", "application/json", strings.NewReader(`{"usuario":"op","password":"secreta"}`))
	res.Body.Close()
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == cookieOperador {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatalf("sin cookie HttpOnly de operador")
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/salas", strings.NewReader(cuerpo))
	req.AddCookie(cookie)
	res, _ = http.DefaultClient.Do(req)
	var creada struct {
		Sala  Sala   `json:"sala"`
		Token string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&creada)
	res.Body.Close()
	if res.StatusCode != 201 || creada.Token == "" || creada.Sala.ID == "" {
		t.Fatalf("crear sala con sesión → %d %+v", res.StatusCode, creada)
	}

	// Una cookie adulterada no entra.
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/estado", nil)
	req.AddCookie(&http.Cookie{Name: cookieOperador, Value: "9999999999.abc"})
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("cookie falsa → %d", res.StatusCode)
	}
}

func TestEmisionConTokenAjenoSeRechazaYConElPropioEntraAudio(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "a", "en")
	e.sala(t, "b", "en")
	ws := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/salas/a/audio"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// El token de la sala b no abre la sala a.
	c, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"token":"`+e.api.firma.TokenEmision("b")+`"}`))
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("token ajeno: esperaba cierre por política, fue %v", err)
	}

	// Con el suyo entra, y el audio llega a redis como tramo.
	c, _, err = websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageText, []byte(`{"token":"`+e.api.firma.TokenEmision("a")+`"}`))
	if _, m, err := c.Read(ctx); err != nil || !strings.Contains(string(m), `"ok":true`) {
		t.Fatalf("saludo: %s %v", m, err)
	}
	r := rand.New(rand.NewSource(9))
	pcm := concat(habla(r, 9), ruido(r, 1))
	for i := 0; i < len(pcm); i += 1600 {
		c.Write(ctx, websocket.MessageBinary, pcmABytes(pcm[i:min(i+1600, len(pcm))]))
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := e.bus.r.XLen(ctx, claveTramos).Result(); n > 0 {
			msgs, _ := e.bus.r.XRange(ctx, claveTramos, "-", "+").Result()
			v := msgs[0].Values
			if v["sala"] != "a" || v["idioma"] != "en" || v["final"] != "1" {
				t.Fatalf("tramo: sala=%v idioma=%v final=%v", v["sala"], v["idioma"], v["final"])
			}
			if pcm, _ := v["pcm"].(string); len(pcm) < 2*8*FrecMuestreo {
				t.Fatalf("el tramo trae %d bytes de PCM; esperaba ≥ 8 s", len(pcm))
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("el audio emitido no produjo ningún tramo en redis")
}

// ---- salud ---------------------------------------------------------------------

func TestHealthzDiceSiElMotorLate(t *testing.T) {
	e := nuevoEntorno(t)
	get := func() int {
		res, _ := http.Get(e.srv.URL + "/api/healthz")
		res.Body.Close()
		return res.StatusCode
	}
	if c := get(); c != 503 {
		t.Fatalf("sin motor → %d, esperaba 503", c)
	}
	e.bus.r.HSet(e.ctx, claveMotor, "latido", time.Now().Add(-90*time.Second).UnixMilli(), "gpu", "ok")
	if c := get(); c != 200 {
		t.Fatalf("latido de hace 90 s (un Recreate) → %d: degradado no es muerto", c)
	}
	e.bus.r.HSet(e.ctx, claveMotor, "latido", time.Now().Add(-5*time.Minute).UnixMilli())
	if c := get(); c != 503 {
		t.Fatalf("latido de hace 5 min → %d, esperaba 503", c)
	}
}

// ---- export -----------------------------------------------------------------------

func TestExportSRTPartidoYEnElIdiomaPedido(t *testing.T) {
	largo := "This is a fairly long sentence that the speaker said in one breath and it must be split into readable cues for the audience"
	subs := []Subtitulo{
		{Tipo: "final", Orig: largo, Idioma: "en", Es: "Frase traducida", TIni: 10_000, TFin: 20_000},
		{Tipo: "parcial", Orig: "no va", Idioma: "en", TIni: 20_000, TFin: 22_000},
		{Tipo: "final", Orig: "Second.", Idioma: "en", Es: "Segunda.", TIni: 21_000, TFin: 30_000},
	}
	srt := SRT(cues(subs, "en"))
	if !strings.HasPrefix(srt, "1\n00:00:00,000 --> ") {
		t.Fatalf("el primer cue tiene que empezar en cero:\n%s", srt)
	}
	if strings.Contains(srt, "no va") {
		t.Fatal("un parcial terminó en el export")
	}
	for _, bloque := range strings.Split(strings.TrimSpace(srt), "\n\n") {
		lineas := strings.Split(bloque, "\n")[2:]
		for _, l := range lineas {
			if len(l) > 60 {
				t.Errorf("renglón de %d caracteres: %q", len(l), l)
			}
		}
		if len(lineas) > 2 {
			t.Errorf("cue de %d renglones", len(lineas))
		}
	}
	if !strings.Contains(srt, "00:00:11,000 --> 00:00:20,000\nSecond.") {
		t.Errorf("el segundo tramo no quedó a los 11 s del primero:\n%s", srt)
	}
	es := VTT(cues(subs, "es"))
	if !strings.HasPrefix(es, "WEBVTT\n\n") || !strings.Contains(es, "Segunda.") || strings.Contains(es, "Second.") {
		t.Fatalf("VTT en español:\n%s", es)
	}
}

// Golpes cortos (un micrófono que se toca, una puerta) abren un tramo pero
// no tienen voz suficiente: no se mandan al motor, donde Whisper los
// "transcribiría" con una frase inventada.
func TestGolpesCortosNoSeMandan(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	var partes [][]int16
	for i := 0; i < 6; i++ {
		partes = append(partes, ruido(r, 2), habla(r, 0.12))
	}
	partes = append(partes, ruido(r, 3))
	if tr := segmentar(t, ConfigPorDefecto(), concat(partes...)); len(tr) != 0 {
		t.Fatalf("6 golpes de 120 ms dieron %d tramos", len(tr))
	}
}

// leerSSEDurante junta los eventos (menos estado) que llegan en d.
func leerSSEDurante(t *testing.T, url, ultimo string, d time.Duration) []eventoSSE {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Last-Event-ID", ultimo)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out []eventoSSE
	var ev eventoSSE
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "id: "):
			ev.id = l[4:]
		case strings.HasPrefix(l, "event: "):
			ev.evento = l[7:]
		case strings.HasPrefix(l, "data: "):
			ev.datos = l[6:]
		case l == "":
			if ev.evento == "final" || ev.evento == "parcial" {
				out = append(out, ev)
			}
			ev = eventoSSE{}
		}
	}
	return out
}

// El peor caso del replay: lo mismo llega por los dos caminos (el replay
// desde Last-Event-ID y el lector que reparte en vivo). Cada línea se ve
// UNA vez: una audiencia sorda leyendo la misma frase dos veces no sabe si
// el orador la repitió.
func TestReplayYVivoSolapadosNoDuplican(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "s3", "en")
	id1 := e.sub(t, "s3", "uno")
	e.sub(t, "s3", "dos")
	e.sub(t, "s3", "tres")

	// Un lector que arranca desde el principio, a mano, DESPUÉS de que el
	// espectador se suscribió: repartirá uno, dos y tres en vivo mientras
	// el replay manda dos y tres.
	e.hub.mu.Lock()
	ctx, cancel := context.WithCancel(e.ctx)
	e.hub.lectores["s3"] = cancel
	e.hub.mu.Unlock()
	go func() {
		for e.hub.difusor("s3").Espectadores() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		e.hub.Leer(ctx, "s3", "0-0")
	}()

	ev := leerSSEDurante(t, e.srv.URL+"/api/salas/s3/subtitulos", id1, 800*time.Millisecond)
	var textos []string
	for _, x := range ev {
		var s Subtitulo
		json.Unmarshal([]byte(x.datos), &s)
		textos = append(textos, s.Orig)
	}
	if strings.Join(textos, ",") != "dos,tres" {
		t.Fatalf("llegó %v; esperaba exactamente [dos tres]", textos)
	}
}

// Con el audio REAL de la demo (130 s de charla de Nerdearla 2025 por
// idioma): el sintético no tiene respiraciones, muletillas ni aplausos.
func TestSegmentadorSobreLaDemoReal(t *testing.T) {
	for nombre, pista := range map[string][]byte{"en": demoEN, "es": demoES} {
		tr := segmentar(t, ConfigPorDefecto(), bytesAPCM(pista))
		var total time.Duration
		var durs []string
		for i, x := range tr {
			d := durTramo(x)
			total += d
			durs = append(durs, d.Round(100*time.Millisecond).String())
			if d > 12*time.Second {
				t.Errorf("%s: tramo %d dura %s", nombre, i, d)
			}
		}
		t.Logf("%s: %d tramos, %s de audio en tramos de 130 s: %v", nombre, len(tr), total.Round(time.Second), durs)
		// 130 s de charla hablada: casi todo tiene que terminar en algún
		// tramo; perder más de 20 s sería perder frases.
		if total < 110*time.Second {
			t.Errorf("%s: sólo %s de 130 s quedó en tramos", nombre, total)
		}
	}
}

// Ningún cue queda con una palabra suelta: el primer export real partió
// «…están borrosas ahora porque» en 80 caracteres + «porque» (0,8 s).
func TestPartirReparteParejo(t *testing.T) {
	texto := "Dios mío. Me voy a casa. Así que puedes ver que las líneas están borrosas ahora porque"
	partes := partir(texto)
	if len(partes) != 2 {
		t.Fatalf("esperaba 2 cues, hubo %d: %q", len(partes), partes)
	}
	for _, p := range partes {
		if len(p) < 30 || len(p) > maxCue {
			t.Errorf("cue de %d caracteres: %q", len(p), p)
		}
	}
	if strings.Join(partes, " ") != texto {
		t.Fatalf("se perdió texto: %q", partes)
	}
	if got := partir("Corto."); len(got) != 1 || got[0] != "Corto." {
		t.Fatalf("texto corto: %q", got)
	}
}

// El contrato con conf-motor, fijado de ESTE lado: lo que PublicarTramo
// escribe es exactamente CamposTramo, y CamposSub es lo que subDeMensaje
// lee. El gemelo está en conf-motor (tests/test_motor.py::TestBusDeVerdad).
func TestContrato(t *testing.T) {
	e := nuevoEntorno(t)
	s := e.sala(t, "c1", "es")
	s.Glosario = "Javier Tebas, midudev"
	e.bus.GuardarSala(e.ctx, s)
	if err := e.bus.PublicarTramo(e.ctx, s, Tramo{PCM: []int16{1, 2}, Seq: 3, Final: true,
		Inicio: time.UnixMilli(1000), Fin: time.UnixMilli(2000)}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := e.bus.r.XRange(e.ctx, claveTramos, "-", "+").Result()
	var campos []string
	for k := range msgs[0].Values {
		campos = append(campos, k)
	}
	slices.Sort(campos)
	esperado := slices.Clone(CamposTramo)
	slices.Sort(esperado)
	if !slices.Equal(campos, esperado) {
		t.Fatalf("PublicarTramo escribe %v; el contrato dice %v", campos, esperado)
	}
	if msgs[0].Values["glosario"] != "Javier Tebas, midudev" {
		t.Fatalf("el glosario de la sala no viajó en el tramo: %v", msgs[0].Values["glosario"])
	}
	if !slices.Equal(CamposTramo, []string{"sala", "idioma", "backend", "seq", "final", "t_ini", "t_fin", "glosario", "pcm"}) ||
		!slices.Equal(CamposSub, []string{"tipo", "orig", "idioma", "es", "en", "t_ini", "t_fin", "backend", "lat_ms", "seq", "detalle"}) {
		t.Fatal("el contrato cambió: cambialo también en conf-motor (motor/bus.py) y en su test")
	}
}

func TestGlosarioSeNormalizaYSeAcota(t *testing.T) {
	g, ok := normalizarGlosario("  Javier   Tebas ,, midudev,  Nerdearla ")
	if !ok || g != "Javier Tebas, midudev, Nerdearla" {
		t.Fatalf("%q %v", g, ok)
	}
	largo := strings.Repeat("x,", 21)
	if _, ok := normalizarGlosario(largo); ok {
		t.Fatal("21 términos pasaron el tope de 20")
	}
}

// leerHasta lee eventos SSE de res durante d y llama a f con cada uno.
func leerHasta(res *http.Response, d time.Duration, f func(eventoSSE)) {
	fin := time.AfterFunc(d, func() { res.Body.Close() })
	defer fin.Stop()
	var ev eventoSSE
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "id: "):
			ev.id = l[4:]
		case strings.HasPrefix(l, "event: "):
			ev.evento = l[7:]
		case strings.HasPrefix(l, "data: "):
			ev.datos = l[6:]
		case l == "":
			if ev.evento != "" {
				f(ev)
			}
			ev = eventoSSE{}
		}
	}
}

// Con ventana, la respuesta SSE TERMINA (un proxy que bufferiza la suelta)
// y la reconexión con Last-Event-ID sigue sin perder ni repetir.
func TestSSEConVentanaTerminaYSeRetoma(t *testing.T) {
	e := nuevoEntorno(t)
	e.hub.cfg.VentanaSSE = 300 * time.Millisecond
	e.sala(t, "v1", "en")
	id1 := e.sub(t, "v1", "uno")
	e.sub(t, "v1", "dos")
	t0 := time.Now()
	// Cliente con tope: si la ventana no cierra, el test FALLA en 3 s en
	// vez de colgar el build.
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get(e.srv.URL + "/api/salas/v1/subtitulos")
	if err != nil {
		t.Fatal(err)
	}
	cuerpo, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("la respuesta SSE no terminó en 3 s: la ventana no cierra (%v)", err)
	}
	res.Body.Close()
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("la respuesta tardó %s en terminar; la ventana es 300 ms", d)
	}
	if !strings.Contains(string(cuerpo), "retry: 50") || !strings.Contains(string(cuerpo), `"orig":"dos"`) {
		t.Fatalf("cuerpo: %q", cuerpo)
	}
	ev := leerSSEDurante(t, e.srv.URL+"/api/salas/v1/subtitulos", id1, time.Second)
	if len(ev) != 1 || !strings.Contains(ev[0].datos, `"orig":"dos"`) {
		t.Fatalf("la reconexión desde «uno» tenía que traer sólo «dos»: %+v", ev)
	}
}

// Un segmentador nuevo para la misma sala (hub reiniciado, emisor que
// reconecta, demo que vuelve a tener público) numera POR ENCIMA del
// anterior: si no, el motor y el navegador descartan sus parciales como
// "de un tramo cuyo final ya salió".
func TestSeqCreceEntreSegmentadores(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	pcm := concat(habla(r, 9), ruido(r, 1), habla(r, 9), ruido(r, 2))
	var ultimo int64
	uno := NuevoSegmentador(ConfigPorDefecto(), time.Unix(1_700_000_000, 0), func(tr Tramo) { ultimo = tr.Seq })
	uno.Escribir(pcm)
	if ultimo == 0 {
		t.Fatal("el primer segmentador no emitió")
	}
	// El segundo nace 20 s después (lo que duró el audio del primero).
	var primero int64
	dos := NuevoSegmentador(ConfigPorDefecto(), time.Unix(1_700_000_020, 0), func(tr Tramo) {
		if primero == 0 {
			primero = tr.Seq
		}
	})
	dos.Escribir(pcm)
	if primero <= ultimo {
		t.Fatalf("el segmentador nuevo empezó en seq %d, sin superar el %d del anterior", primero, ultimo)
	}
}

// El error de una sala se borra cuando la sala vuelve a dar subtítulos: si
// no, el panel la mostraba en rojo para siempre (demo-b, 25-09).
func TestElErrorSeBorraCuandoLaSalaSeRecupera(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "er", "es")
	if err := e.hub.AsegurarLector("er"); err != nil {
		t.Fatal(err)
	}
	e.bus.PublicarSubtitulo(e.ctx, "er", Subtitulo{Tipo: "error", Detalle: "No salió el subtítulo: Gemini no respondió a tiempo."})
	deadline := time.Now().Add(2 * time.Second)
	for e.hub.Resumen("er").UltimoError == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r := e.hub.Resumen("er"); r.UltimoError == "" || r.Errores != 1 {
		t.Fatalf("el error no se registró: %+v", r)
	}
	e.sub(t, "er", "volvió")
	deadline = time.Now().Add(2 * time.Second)
	for e.hub.Resumen("er").UltimoError != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r := e.hub.Resumen("er")
	if r.UltimoError != "" {
		t.Fatalf("la sala se recuperó y el error sigue: %q", r.UltimoError)
	}
	if r.Errores != 1 {
		t.Fatalf("el contador es historia y se conserva: %d", r.Errores)
	}
}

func TestSalasDemoConGlosarioYNombresNumerados(t *testing.T) {
	e := nuevoEntorno(t)
	e.hub.cfg.SalasDemo = 4
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	if err := e.hub.PrepararDemo(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := e.bus.Sala(e.ctx, "demo-a")
	d, _ := e.bus.Sala(e.ctx, "demo-d")
	if !strings.HasPrefix(a.Nombre, "Sala 01 · ") || !strings.Contains(a.Glosario, "ElevenLabs") {
		t.Fatalf("demo-a: %+v", a)
	}
	if d.Idioma != "es" || !strings.Contains(d.Glosario, "Javier Tebas") {
		t.Fatalf("demo-d: %+v", d)
	}
	if idDemo(26) != "27" {
		t.Fatalf("después de la z: %q", idDemo(26))
	}
}
