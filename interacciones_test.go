package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// cliente con su propia cookie de visitante: cada uno es "otra persona".
func cliente(t *testing.T) *http.Client {
	j, _ := cookiejar.New(nil)
	return &http.Client{Jar: j, Timeout: 5 * time.Second}
}

func postJSON(t *testing.T, c *http.Client, url, cuerpo string) (*http.Response, map[string]any) {
	t.Helper()
	r, err := c.Post(url, "application/json", strings.NewReader(cuerpo))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	return r, m
}

func operador(t *testing.T, e *entornoPrueba) *http.Client {
	c := cliente(t)
	r, _ := postJSON(t, c, e.srv.URL+"/api/sesion", `{"usuario":"op","password":"secreta"}`)
	if r.StatusCode != 200 {
		t.Fatalf("login: %d", r.StatusCode)
	}
	return c
}

func TestReaccionesSeAgreganYNoSeEntiendeNoSeReparte(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "r1", "en")
	url := e.srv.URL + "/api/salas/r1/subtitulos"
	ch := make(chan []eventoSSE, 1)
	go func() {
		req, _ := http.NewRequest("GET", url, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- nil
			return
		}
		defer res.Body.Close()
		var out []eventoSSE
		leerHasta(res, 600*time.Millisecond, func(ev eventoSSE) {
			if ev.evento == "reacciones" {
				out = append(out, ev)
			}
		})
		ch <- out
	}()
	for e.hub.difusor("r1").Espectadores() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		postJSON(t, cliente(t), e.srv.URL+"/api/salas/r1/reacciones", `{"id":"aplauso"}`)
	}
	postJSON(t, cliente(t), e.srv.URL+"/api/salas/r1/reacciones", `{"id":"noseentiende"}`)
	evs := <-ch
	total := 0
	for _, ev := range evs {
		var m map[string]int
		json.Unmarshal([]byte(ev.datos), &m)
		total += m["aplauso"]
		if _, hay := m["noseentiende"]; hay {
			t.Fatal("'no se entiende' llegó a la sala: es una señal para producción, no para la audiencia")
		}
	}
	if total != 3 || len(evs) > 2 {
		t.Fatalf("3 aplausos en %d eventos, total %d: tienen que llegar juntos", len(evs), total)
	}
	if n := e.api.interaccion(e.ctx, "r1").NoSeEntiende; n != 1 {
		t.Fatalf("el panel cuenta %d 'no se entiende', esperaba 1", n)
	}
	if r, _ := postJSON(t, cliente(t), e.srv.URL+"/api/salas/r1/reacciones", `{"id":"insulto"}`); r.StatusCode != 400 {
		t.Fatalf("reacción inventada → %d", r.StatusCode)
	}
}

func TestTopePorVisitante(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "r2", "en")
	c := cliente(t)
	var codigos []int
	for i := 0; i < 8; i++ {
		r, _ := postJSON(t, c, e.srv.URL+"/api/salas/r2/reacciones", `{"id":"fuego"}`)
		codigos = append(codigos, r.StatusCode)
	}
	if codigos[0] != 204 || codigos[5] != 204 || codigos[6] != 429 {
		t.Fatalf("ráfaga de 6 y después 429; fue %v", codigos)
	}
	// Otra persona en la misma red no queda frenada por la primera.
	if r, _ := postJSON(t, cliente(t), e.srv.URL+"/api/salas/r2/reacciones", `{"id":"fuego"}`); r.StatusCode != 204 {
		t.Fatalf("un vecino de red quedó frenado: %d", r.StatusCode)
	}
}

func listar(t *testing.T, c *http.Client, url string) []Pregunta {
	t.Helper()
	r, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var ps []Pregunta
	json.NewDecoder(r.Body).Decode(&ps)
	return ps
}

func TestPreguntaPendienteModeradaYVotada(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "q1", "en")
	base := e.srv.URL + "/api/salas/q1/preguntas"
	autor, otro, op := cliente(t), cliente(t), operador(t, e)

	r, p := postJSON(t, autor, base, `{"texto":"  ¿Corre en una   GPU más chica?  "}`)
	if r.StatusCode != 201 || p["estado"] != "pendiente" || p["texto"] != "¿Corre en una GPU más chica?" {
		t.Fatalf("preguntar: %d %v", r.StatusCode, p)
	}
	pid := p["id"].(string)

	if ps := listar(t, otro, base); len(ps) != 0 {
		t.Fatalf("otra persona ve una pregunta sin moderar: %v", ps)
	}
	if ps := listar(t, autor, base); len(ps) != 1 || !ps[0].Mia {
		t.Fatalf("quien la escribió tiene que verla pendiente: %v", ps)
	}
	if ps := listar(t, op, base+"?todas=1"); len(ps) != 1 {
		t.Fatalf("producción no ve la pendiente: %v", ps)
	}
	if ps := listar(t, otro, base+"?todas=1"); len(ps) != 0 {
		t.Fatal("?todas=1 sin sesión de operador mostró pendientes")
	}
	// Votar una pendiente: no.
	if r, _ := postJSON(t, otro, base+"/"+pid+"/voto", ``); r.StatusCode != 404 {
		t.Fatalf("voto a una pendiente → %d", r.StatusCode)
	}
	// Moderar sin sesión: no.
	req, _ := http.NewRequest("PATCH", base+"/"+pid, strings.NewReader(`{"estado":"publicada"}`))
	if r, _ := otro.Do(req); r.StatusCode != 401 {
		t.Fatalf("moderar sin sesión → %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PATCH", base+"/"+pid, strings.NewReader(`{"estado":"publicada"}`))
	if r, _ := op.Do(req); r.StatusCode != 200 {
		t.Fatalf("publicar → %d", r.StatusCode)
	}
	// Voto, y el mismo visitante votando otra vez lo QUITA (no suma dos).
	_, v := postJSON(t, otro, base+"/"+pid+"/voto", ``)
	if v["votos"].(float64) != 1 || v["votada"] != true {
		t.Fatalf("primer voto: %v", v)
	}
	_, v = postJSON(t, otro, base+"/"+pid+"/voto", ``)
	if v["votos"].(float64) != 0 || v["votada"] != false {
		t.Fatalf("segundo toque del mismo visitante: %v", v)
	}
	// El autor nunca sale por la API.
	r2, _ := otro.Get(base)
	var crudo []map[string]any
	json.NewDecoder(r2.Body).Decode(&crudo)
	r2.Body.Close()
	if _, hay := crudo[0]["autor"]; hay || len(crudo) != 1 {
		t.Fatalf("la lista pública expone el autor o no trae la publicada: %v", crudo)
	}
	if r, _ := postJSON(t, cliente(t), base, `{"texto":"hey"}`); r.StatusCode != 400 {
		t.Fatalf("pregunta de 3 letras → %d", r.StatusCode)
	}
}

func TestSugerenciaAprobadaEntraAlGlosario(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "g1", "es")
	base := e.srv.URL + "/api/salas/g1/sugerencias"
	postJSON(t, cliente(t), base, `{"termino":"Javier Tebas"}`)
	postJSON(t, cliente(t), base, `{"termino":"javier tebas"}`)
	if r, _ := postJSON(t, cliente(t), base, `{"termino":"a,b"}`); r.StatusCode != 400 {
		t.Fatalf("término con coma → %d", r.StatusCode)
	}
	if r, _ := cliente(t).Get(base); r.StatusCode != 401 {
		t.Fatalf("la lista de sugerencias sin sesión → %d", r.StatusCode)
	}
	op := operador(t, e)
	r, _ := op.Get(base)
	var sg []Sugerencia
	json.NewDecoder(r.Body).Decode(&sg)
	r.Body.Close()
	if len(sg) != 1 || sg[0].Veces != 2 {
		t.Fatalf("la misma sugerencia dos veces tiene que sumar, no duplicar: %v", sg)
	}
	postJSON(t, op, base+"/resolver", `{"termino":"Javier Tebas","aprobar":true}`)
	s, _ := e.bus.Sala(e.ctx, "g1")
	if s.Glosario != "Javier Tebas" {
		t.Fatalf("glosario: %q", s.Glosario)
	}
	if e.api.interaccion(e.ctx, "g1").Sugerencias != 0 {
		t.Fatal("la sugerencia resuelta sigue en la lista")
	}
}

func TestBandejaJuntaTodasLasSalasYSoloParaProduccion(t *testing.T) {
	e := nuevoEntorno(t)
	e.sala(t, "b1", "en")
	e.sala(t, "b2", "es")
	postJSON(t, cliente(t), e.srv.URL+"/api/salas/b1/preguntas", `{"texto":"primera, en la sala uno"}`)
	time.Sleep(5 * time.Millisecond)
	postJSON(t, cliente(t), e.srv.URL+"/api/salas/b2/preguntas", `{"texto":"segunda, en la sala dos"}`)
	postJSON(t, cliente(t), e.srv.URL+"/api/salas/b2/sugerencias", `{"termino":"midudev"}`)
	if r, _ := cliente(t).Get(e.srv.URL + "/api/bandeja"); r.StatusCode != 401 {
		t.Fatalf("bandeja sin sesión → %d", r.StatusCode)
	}
	r, _ := operador(t, e).Get(e.srv.URL + "/api/bandeja")
	var b struct {
		Preguntas   []ItemBandeja    `json:"preguntas"`
		Sugerencias []SugerenciaSala `json:"sugerencias"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	r.Body.Close()
	if len(b.Preguntas) != 2 || b.Preguntas[0].Sala != "b1" || b.Preguntas[1].Sala != "b2" {
		t.Fatalf("la bandeja tiene que traer las dos salas, la más vieja primero: %+v", b.Preguntas)
	}
	if len(b.Sugerencias) != 1 || b.Sugerencias[0].Sala != "b2" || b.Sugerencias[0].Termino != "midudev" {
		t.Fatalf("sugerencias: %+v", b.Sugerencias)
	}
}
