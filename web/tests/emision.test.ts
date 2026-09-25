import { describe, expect, it } from "vitest";
import { conexionMuerta, duracion, espera, recordada, recordar, Silencio } from "../src/lib/emision";

describe("espera entre reintentos", () => {
  it("crece y se queda en 5 s: un corte largo no deja la sala sin intentar", () => {
    expect([0, 1, 2, 3, 4, 10].map(espera)).toEqual([500, 1000, 2000, 4000, 5000, 5000]);
  });
});

describe("conexión muerta sin cierre", () => {
  it("5 s sin latido del hub es muerta aunque el socket diga abierto", () => {
    expect(conexionMuerta(0, 4900)).toBe(false);
    expect(conexionMuerta(0, 5100)).toBe(true);
  });
});

describe("silencio", () => {
  it("alarma tras un minuto de silencio seguido, y el sonido lo reinicia", () => {
    const s = new Silencio();
    s.registrar(-90, 0);
    s.registrar(-90, 59_000);
    expect(s.alarma(59_000)).toBe(false);
    expect(s.alarma(60_000)).toBe(true);
    s.registrar(-20, 61_000);
    expect(s.hace(61_000)).toBe(0);
    expect(s.alarma(200_000)).toBe(false);
  });
});

describe("recordar la fuente", () => {
  it("vuelve a emitir tras un F5 sólo si se estaba emitiendo el micrófono", () => {
    const m = new Map<string, string>();
    const al = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
    expect(recordada("s1", al)).toBe(null);
    recordar("s1", "microfono", al);
    expect(recordada("s1", al)).toBe("microfono");
    expect(recordada("s2", al)).toBe(null);
    recordar("s1", null, al);
    expect(recordada("s1", al)).toBe(null);
  });
  it("sin almacenamiento (modo privado) no rompe", () => {
    const roto = { getItem: () => { throw new Error("x"); }, setItem: () => { throw new Error("x"); }, removeItem: () => { throw new Error("x"); } };
    expect(() => recordar("s", "microfono", roto)).not.toThrow();
    expect(recordada("s", roto)).toBe(null);
  });
});

describe("duración", () => {
  it("se lee de un vistazo", () => {
    expect(duracion(42_000)).toBe("42 s");
    expect(duracion(125_000)).toBe("2 min 5 s");
    expect(duracion(3_725_000)).toBe("1 h 2 min");
  });
});
