package main

// El front de conf (Astro estático, en web/) servido por el propio hub.
//
// POR QUÉ ACÁ Y NO EN UN SERVICIO APARTE: la organización `conf` tiene al
// hub público en `/` (contrato de plataforma, 25-09). El build de Astro se
// hace con `npm run build` en web/ y su salida (web/dist) viaja en el repo;
// el binario la embebe. hub_test.go verifica que las páginas estén y que
// los assets lleven hash, así que un dist viejo o incompleto no pasa el
// build de la imagen.
//
// Las reglas son las de serve.mjs de la plantilla static, más dos:
//   - /_astro/* lleva hash en el nombre → caché inmutable de un año;
//     todo lo demás (HTML, el worklet de audio) → no-cache. Si no, un
//     despliegue queda tapado horas por el borde (guía 03, fallo 6).
//   - /s/<sala> es la dirección corta del QR del cartel → 302 a la sala.

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:web/dist
var distFS embed.FS

var salaCorta = regexp.MustCompile(`^/s/([a-z0-9][a-z0-9-]{0,39})/?$`)

func init() {
	// El alpine de la imagen no trae /etc/mime.types: sin esto un .js
	// saldría como application/octet-stream y el navegador no lo ejecuta.
	for ext, t := range map[string]string{
		".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8",
		".svg": "image/svg+xml", ".woff2": "font/woff2", ".json": "application/json",
		".txt": "text/plain; charset=utf-8", ".webmanifest": "application/manifest+json",
		".png": "image/png", ".ico": "image/x-icon",
	} {
		mime.AddExtensionType(ext, t)
	}
}

func front() http.Handler {
	raiz, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		panic(err)
	}
	existe := func(p string) bool {
		f, err := raiz.Open(p)
		if err != nil {
			return false
		}
		st, err := f.Stat()
		f.Close()
		return err == nil && !st.IsDir()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
			return
		}
		if m := salaCorta.FindStringSubmatch(r.URL.Path); m != nil {
			w.Header().Set("Cache-Control", "no-cache")
			http.Redirect(w, r, "/sala/?s="+m[1], http.StatusFound)
			return
		}
		limpio := path.Clean("/" + r.URL.Path)
		rel := strings.TrimPrefix(limpio, "/")
		switch {
		case rel == "":
			rel = "index.html"
		case existe(rel):
		case existe(rel + "/index.html"):
			if !strings.HasSuffix(r.URL.Path, "/") {
				// /sala → /sala/ conservando la query (?s=…&l=…).
				dest := limpio + "/"
				if r.URL.RawQuery != "" {
					dest += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, dest, http.StatusMovedPermanently)
				return
			}
			rel += "/index.html"
		default:
			w.Header().Set("Cache-Control", "no-cache")
			http.Error(w, "no existe", http.StatusNotFound)
			return
		}
		datos, err := fs.ReadFile(raiz, rel)
		if err != nil {
			http.Error(w, "no existe", http.StatusNotFound)
			return
		}
		if t := mime.TypeByExtension(path.Ext(rel)); t != "" {
			w.Header().Set("Content-Type", t)
		}
		if strings.HasPrefix(rel, "_astro/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(datos)
	})
}
