package main

// Dos puertas, y ninguna es un decorado (guía 06: un permiso se comprueba en
// la API, no escondiendo un enlace):
//
//   - el PANEL de producción: usuario y clave del operador (secreto
//     hub-operador) → cookie firmada, HttpOnly, 12 h;
//   - la EMISIÓN de audio de una sala: un token por sala derivado de la
//     clave de firma. Sin él, cualquiera con la URL podría meter audio en
//     una sala pública y hacerle decir cualquier cosa a los subtítulos.
//
// La audiencia no necesita nada: mirar subtítulos es público.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	cookieOperador = "conf_op"
	duracionSesion = 12 * time.Hour
)

type Firma struct{ clave []byte }

func (f Firma) firmar(msg string) string {
	m := hmac.New(sha256.New, f.clave)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// TokenEmision es estable por sala: el operador lo puede volver a pedir y
// el enlace de /emitir que ya repartió sigue valiendo.
func (f Firma) TokenEmision(sala string) string {
	return f.firmar("emitir:" + sala)[:32]
}

func (f Firma) TokenValido(sala, token string) bool {
	return subtle.ConstantTimeCompare([]byte(f.TokenEmision(sala)), []byte(token)) == 1
}

func (f Firma) Sesion(ahora time.Time) string {
	exp := strconv.FormatInt(ahora.Add(duracionSesion).Unix(), 10)
	return exp + "." + f.firmar("op:"+exp)
}

func (f Firma) SesionValida(valor string, ahora time.Time) bool {
	exp, firma, ok := strings.Cut(valor, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(f.firmar("op:"+exp)), []byte(firma)) != 1 {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && ahora.Unix() < n
}

func igualConstante(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (a *API) esOperador(r *http.Request) bool {
	c, err := r.Cookie(cookieOperador)
	return err == nil && a.firma.SesionValida(c.Value, a.hub.ahora())
}

// soloOperador envuelve un handler del panel: 401 sin sesión.
func (a *API) soloOperador(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieOperador)
		if err != nil || !a.firma.SesionValida(c.Value, a.hub.ahora()) {
			jsonError(w, http.StatusUnauthorized, "sin_sesion", "entrá al panel primero")
			return
		}
		h(w, r)
	}
}
