import { describe, expect, it } from "vitest";
import { aplicar, hace, leerParametros, PANTALLA_VACIA, textoEn } from "../src/lib/sala";
import type { Subtitulo } from "../src/lib/sala";

const sub = (p: Partial<Subtitulo>): Subtitulo => ({
  id: "1-0", tipo: "final", orig: "hello", idioma: "en", es: "hola", en: "hello",
  t_ini: 0, t_fin: 1000, backend: "gpu", lat_ms: 400, seq: 1, ...p,
});

describe("aplicar", () => {
  it("un final agrega su línea en el idioma elegido", () => {
    const p = aplicar(PANTALLA_VACIA, sub({}), "es");
    expect(p.lineas.map((l) => l.texto)).toEqual(["hola"]);
  });

  it("el mismo final dos veces (replay tras reconectar) no se duplica", () => {
    let p = aplicar(PANTALLA_VACIA, sub({}), "es");
    p = aplicar(p, sub({}), "es");
    expect(p.lineas).toHaveLength(1);
  });

  it("un parcial se muestra como provisional y su final lo borra", () => {
    let p = aplicar(PANTALLA_VACIA, sub({ id: "", tipo: "parcial", seq: 2, orig: "hel", es: "" }), "en");
    expect(p.provisional).toBe("hel");
    p = aplicar(p, sub({ id: "2-0", seq: 2 }), "en");
    expect(p.provisional).toBe("");
    expect(p.lineas.map((l) => l.texto)).toEqual(["hello"]);
  });

  it("un parcial de un tramo cuyo final ya llegó no vuelve a aparecer", () => {
    let p = aplicar(PANTALLA_VACIA, sub({ id: "3-0", seq: 3 }), "en");
    p = aplicar(p, sub({ id: "", tipo: "parcial", seq: 3, orig: "viejo" }), "en");
    expect(p.provisional).toBe("");
  });

  it("un final sin traducción no agrega una línea en blanco", () => {
    const p = aplicar(PANTALLA_VACIA, sub({ es: "" }), "es");
    expect(p.lineas).toHaveLength(0);
  });
});

describe("textoEn", () => {
  it("el idioma de la charla devuelve el original, el otro la traducción", () => {
    expect(textoEn(sub({ idioma: "es", orig: "hola", en: "hi" }), "es")).toBe("hola");
    expect(textoEn(sub({ idioma: "es", orig: "hola", en: "hi" }), "en")).toBe("hi");
  });
});

describe("leerParametros", () => {
  it("rechaza salas con caracteres raros y idiomas desconocidos", () => {
    expect(leerParametros("?s=../../etc&l=fr")).toEqual({ sala: "", idioma: null });
    expect(leerParametros("?s=demo-a&l=en")).toEqual({ sala: "demo-a", idioma: "en" });
  });
});

describe("hace", () => {
  it("dice de cuándo es una línea vieja", () => {
    expect(hace(10_000)).toBe("ahora");
    expect(hace(3 * 60_000)).toBe("hace 3 min");
  });
});

import { mezclarPreguntas } from "../src/lib/sala";
import type { PreguntaT } from "../src/lib/sala";

describe("mezclarPreguntas", () => {
  const p = (id: string, estado: PreguntaT["estado"], extra: Partial<PreguntaT> = {}): PreguntaT =>
    ({ id, texto: id, estado, creada: 0, votos: 0, votada: false, ...extra });

  it("mis pendientes arriba, las públicas con MI voto (el evento no lo trae)", () => {
    const r = mezclarPreguntas([p("a", "publicada"), p("b", "publicada")], [p("m", "pendiente", { mia: true })], new Set(["b"]));
    expect(r.map((x) => x.id)).toEqual(["m", "a", "b"]);
    expect(r.find((x) => x.id === "b")?.votada).toBe(true);
    expect(r.find((x) => x.id === "a")?.votada).toBe(false);
  });

  it("una pendiente mía que ya se publicó aparece una vez, como publicada", () => {
    const r = mezclarPreguntas([p("m", "publicada")], [p("m", "pendiente", { mia: true })], new Set());
    expect(r).toHaveLength(1);
    expect(r[0].estado).toBe("publicada");
  });
});
