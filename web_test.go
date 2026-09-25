package main

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

// El front embebido: si web/dist falta, está a medias o sin hash, la
// imagen no se construye (este test corre en el Containerfile).
func TestFrontEmbebidoCompleto(t *testing.T) {
	for _, p := range []string{"index.html", "sala/index.html", "obs/index.html", "emitir/index.html",
		"panel/index.html", "cartel/index.html", "orador/index.html", "pcm-worklet.js"} {
		if _, err := fs.Stat(distFS, "web/dist/"+p); err != nil {
			t.Errorf("falta %s en web/dist: corré `npm run build` en web/ y commiteá dist", p)
		}
	}
	js := 0
	fs.WalkDir(distFS, "web/dist/_astro", func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".js") {
			js++
		}
		return nil
	})
	if js == 0 {
		t.Error("web/dist/_astro no tiene JS: el build de Astro no está")
	}
}

func TestFrontSirveConLaCacheCorrecta(t *testing.T) {
	e := nuevoEntorno(t)
	get := func(ruta string) (*http.Response, string) {
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := c.Get(e.srv.URL + ruta)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, string(b)
	}
	r, cuerpo := get("/")
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "text/html") || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("/ → %d %q %q", r.StatusCode, r.Header.Get("Content-Type"), r.Header.Get("Cache-Control"))
	}
	var asset string
	fs.WalkDir(distFS, "web/dist/_astro", func(p string, d fs.DirEntry, err error) error {
		if asset == "" && strings.HasSuffix(p, ".js") {
			asset = strings.TrimPrefix(p, "web/dist")
		}
		return nil
	})
	r, _ = get(asset)
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") ||
		!strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("%s → %d %q %q: un .js con hash tiene que ser inmutable y text/javascript", asset,
			r.StatusCode, r.Header.Get("Cache-Control"), r.Header.Get("Content-Type"))
	}
	if r, _ := get("/pcm-worklet.js"); r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("el worklet no tiene hash: no puede ser inmutable")
	}
	if r, _ := get("/sala?s=demo-a&l=es"); r.StatusCode != 301 || r.Header.Get("Location") != "/sala/?s=demo-a&l=es" {
		t.Fatalf("/sala?… → %d %q: la redirección tiene que conservar la query", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := get("/s/demo-a"); r.StatusCode != 302 || r.Header.Get("Location") != "/sala/?s=demo-a" {
		t.Fatalf("/s/demo-a → %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// Rutas con .. : el ServeMux de Go las limpia y redirige (307) a la
	// ruta limpia, que queda DENTRO del sitio y da 404. Se sigue la
	// redirección y se exige el 404 al final: nunca un archivo de afuera.
	for _, ruta := range []string{"/no-existe", "/s/../etc", "/../../etc/passwd", "/s/MAYUS"} {
		r, err := http.Get(e.srv.URL + ruta)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 404 || strings.Contains(string(b), "root:") {
			t.Errorf("%s → %d al final, esperaba 404", ruta, r.StatusCode)
		}
	}
	if r, _ := get("/api/salas"); !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		t.Fatal("el front se comió la API: /api/salas no es JSON")
	}
	_ = cuerpo
}
