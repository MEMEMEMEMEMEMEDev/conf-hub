package main

import (
	"os"
	"testing"
	"time"
)

func TestEsperaCreceYSeQuedaEnCinco(t *testing.T) {
	var got []time.Duration
	for n := 0; n < 6; n++ {
		got = append(got, espera(n))
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("espera = %v, esperaba %v", got, want)
		}
	}
}

func TestURLDeAudio(t *testing.T) {
	u, err := urlAudio("https://conf.aaroidev.com/", "sala-1")
	if err != nil || u != "wss://conf.aaroidev.com/api/salas/sala-1/audio" {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := urlAudio("conf.aaroidev.com", "x"); err == nil {
		t.Fatal("una URL sin esquema tiene que rechazarse")
	}
}

func TestElTokenNuncaVienePorArgv(t *testing.T) {
	os.Unsetenv("CONF_TOKEN")
	if _, err := leerToken(""); err == nil {
		t.Fatal("sin CONF_TOKEN ni archivo tiene que fallar")
	}
	t.Setenv("CONF_TOKEN", " abc ")
	if tok, _ := leerToken(""); tok != "abc" {
		t.Fatalf("token: %q", tok)
	}
}
