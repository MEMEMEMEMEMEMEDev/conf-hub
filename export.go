package main

// Export de la charla entera: SRT, WebVTT o texto plano, en el idioma
// original o en la traducción.
//
// UN TRAMO NO ES UN SUBTÍTULO. Un tramo dura 8 a 12 s y trae dos o tres
// oraciones: como cue único sería un bloque ilegible en pantalla. Se parte
// en cues de hasta ~84 caracteres (dos líneas de 42, la norma de
// subtitulado) y el tiempo del tramo se reparte entre ellos en proporción
// a su largo. No es el tiempo exacto de cada palabra —eso no lo tenemos—
// pero en un reproductor se lee bien, y es honesto con lo que sabemos.
//
// Los nombres siguen la convención `charla.es.srt` que ya reproducen Plex,
// Jellyfin y merpy: una grabación de la charla con este archivo al lado se
// ve subtitulada sin tocar nada.

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const maxCue = 84

type cue struct {
	ini, fin time.Duration
	texto    string
}

func textoEn(s Subtitulo, idioma string) string {
	switch idioma {
	case "es":
		if s.Idioma == "es" {
			return s.Orig
		}
		return s.Es
	case "en":
		if s.Idioma == "en" {
			return s.Orig
		}
		return s.En
	default:
		return s.Orig
	}
}

// partir reparte el texto en cues PAREJOS de hasta maxCue caracteres. No
// llena el primero hasta el tope: así el último no queda con una palabra
// suelta de 0,8 s en pantalla (visto en el primer export real: «porque»).
func partir(texto string) []string {
	palabras := strings.Fields(texto)
	if len(palabras) == 0 {
		return nil
	}
	total := len(strings.Join(palabras, " "))
	n := (total + maxCue - 1) / maxCue
	objetivo := (total + n - 1) / n
	var out []string
	var linea strings.Builder
	for _, p := range palabras {
		quedan := n - len(out) // cues que faltan, contando el actual
		if linea.Len() > 0 && quedan > 1 &&
			(linea.Len()+1+len(p) > maxCue || linea.Len() >= objetivo) {
			out = append(out, linea.String())
			linea.Reset()
		}
		if linea.Len() > 0 {
			linea.WriteByte(' ')
		}
		linea.WriteString(p)
	}
	out = append(out, linea.String())
	return out
}

// dosLineas corta un cue en dos renglones por el espacio más cercano al medio.
func dosLineas(s string) string {
	if len(s) <= 42 {
		return s
	}
	medio := len(s) / 2
	mejor := -1
	for i := range s {
		if s[i] == ' ' && (mejor == -1 || abs(i-medio) < abs(mejor-medio)) {
			mejor = i
		}
	}
	if mejor == -1 {
		return s
	}
	return s[:mejor] + "\n" + s[mejor+1:]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func cues(subs []Subtitulo, idioma string) []cue {
	var finales []Subtitulo
	for _, s := range subs {
		if s.Tipo == "final" && strings.TrimSpace(textoEn(s, idioma)) != "" {
			finales = append(finales, s)
		}
	}
	if len(finales) == 0 {
		return nil
	}
	slices.SortFunc(finales, func(a, b Subtitulo) int { return int(a.TIni - b.TIni) })
	cero := finales[0].TIni
	var out []cue
	for _, s := range finales {
		partes := partir(textoEn(s, idioma))
		total := 0
		for _, p := range partes {
			total += len(p)
		}
		ini := time.Duration(s.TIni-cero) * time.Millisecond
		dur := time.Duration(s.TFin-s.TIni) * time.Millisecond
		t := ini
		for _, p := range partes {
			d := dur * time.Duration(len(p)) / time.Duration(max(total, 1))
			out = append(out, cue{ini: t, fin: t + d, texto: dosLineas(p)})
			t += d
		}
	}
	return out
}

func marcaTiempo(d time.Duration, sep string) string {
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, d/time.Millisecond)
}

func SRT(cs []cue) string {
	var b strings.Builder
	for i, c := range cs {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, marcaTiempo(c.ini, ","), marcaTiempo(c.fin, ","), c.texto)
	}
	return b.String()
}

func VTT(cs []cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", marcaTiempo(c.ini, "."), marcaTiempo(c.fin, "."), c.texto)
	}
	return b.String()
}

func TXT(subs []Subtitulo, idioma string) string {
	var b strings.Builder
	for _, s := range subs {
		if t := strings.TrimSpace(textoEn(s, idioma)); s.Tipo == "final" && t != "" {
			b.WriteString(t)
			b.WriteString("\n")
		}
	}
	return b.String()
}

var formatoValido = regexp.MustCompile(`^(srt|vtt|txt)$`)

// GET /api/salas/{id}/export?formato=srt&idioma=es
func (a *API) exportar(w http.ResponseWriter, r *http.Request) {
	s, ok := a.salaDe(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	formato := q.Get("formato")
	if !formatoValido.MatchString(formato) {
		jsonError(w, http.StatusBadRequest, "formato", "formato: srt, vtt o txt")
		return
	}
	idioma := q.Get("idioma")
	if idioma == "" || idioma == "orig" {
		idioma = s.Idioma
	}
	if !idiomaValido[idioma] {
		jsonError(w, http.StatusBadRequest, "idioma", "idioma: en o es")
		return
	}
	subs, err := a.hub.bus.Todos(r.Context(), s.ID)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "bus", "no llego a redis")
		return
	}
	var cuerpo, tipo string
	switch formato {
	case "srt":
		cuerpo, tipo = SRT(cues(subs, idioma)), "application/x-subrip"
	case "vtt":
		cuerpo, tipo = VTT(cues(subs, idioma)), "text/vtt"
	default:
		cuerpo, tipo = TXT(subs, idioma), "text/plain"
	}
	w.Header().Set("Content-Type", tipo+"; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s.%s"`, s.ID, idioma, formato))
	w.Write([]byte(cuerpo))
}
