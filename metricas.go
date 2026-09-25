package main

// /api/metrics en formato de texto de Prometheus, sin librería: el mismo
// criterio que merpy-catalog (metricas.go). La sala SÍ es una etiqueta —son
// pocas y acotadas por MAX_SALAS—; el texto de un subtítulo jamás.

import (
	"fmt"
	"net/http"
	"strings"
)

func (a *API) metricas(w http.ResponseWriter, r *http.Request) {
	salas, err := a.hub.bus.Salas(r.Context())
	if err != nil {
		http.Error(w, "redis", http.StatusServiceUnavailable)
		return
	}
	var b strings.Builder
	serie := func(nombre, ayuda, tipo string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", nombre, ayuda, nombre, tipo)
	}
	type fila struct {
		s Sala
		v ResumenVivo
		e int
	}
	var filas []fila
	for _, s := range salas {
		filas = append(filas, fila{s, a.hub.Resumen(s.ID), a.hub.difusor(s.ID).Espectadores()})
	}
	etiqueta := func(f fila) string {
		return fmt.Sprintf(`sala=%q,backend=%q,demo="%t"`, f.s.ID, f.s.Backend, f.s.Demo)
	}
	serie("conf_espectadores", "Conexiones SSE abiertas por sala.", "gauge")
	for _, f := range filas {
		fmt.Fprintf(&b, "conf_espectadores{%s} %d\n", etiqueta(f), f.e)
	}
	serie("conf_en_vivo", "1 si la sala recibe audio ahora.", "gauge")
	for _, f := range filas {
		v := 0
		if f.v.EnVivo {
			v = 1
		}
		fmt.Fprintf(&b, "conf_en_vivo{%s} %d\n", etiqueta(f), v)
	}
	serie("conf_tramos_total", "Tramos finales enviados al motor.", "counter")
	for _, f := range filas {
		fmt.Fprintf(&b, "conf_tramos_total{%s} %d\n", etiqueta(f), f.v.Tramos)
	}
	serie("conf_saltados_total", "Tramos que el motor saltó por atraso.", "counter")
	for _, f := range filas {
		fmt.Fprintf(&b, "conf_saltados_total{%s} %d\n", etiqueta(f), f.v.Saltados)
	}
	serie("conf_errores_total", "Tramos que el motor no pudo procesar.", "counter")
	for _, f := range filas {
		fmt.Fprintf(&b, "conf_errores_total{%s} %d\n", etiqueta(f), f.v.Errores)
	}
	serie("conf_latencia_ms", "Del fin del audio del tramo al subtítulo publicado (últimos 200).", "gauge")
	for _, f := range filas {
		fmt.Fprintf(&b, "conf_latencia_ms{%s,q=\"0.5\"} %d\n", etiqueta(f), f.v.P50)
		fmt.Fprintf(&b, "conf_latencia_ms{%s,q=\"0.95\"} %d\n", etiqueta(f), f.v.P95)
	}
	m, err := a.hub.bus.Motor(r.Context(), a.hub.ahora())
	if err == nil {
		serie("conf_motor_vivo", "1 si el motor latió hace menos de 120 s.", "gauge")
		v := 0
		if m.Vivo {
			v = 1
		}
		fmt.Fprintf(&b, "conf_motor_vivo{gpu=%q} %d\n", m.GPU, v)
		serie("conf_motor_cola", "Tramos esperando en el motor.", "gauge")
		fmt.Fprintf(&b, "conf_motor_cola %d\n", m.Cola)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}
