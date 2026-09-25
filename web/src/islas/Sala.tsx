import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  PliegoBoton, PliegoEnVivo, PliegoEstado, PliegoEtiqueta, PliegoFilete, PliegoOnda, PliegoSubtitulos, PliegoTitulo,
} from "@ahroi/foundation/pliego";
import Interacciones from "./Interacciones";
import type { InteraccionesApi } from "./Interacciones";
import { aplicar, escuchar, hace, leerParametros, NOMBRE_IDIOMA, PANTALLA_VACIA, segundos } from "../lib/sala";
import type { EstadoSala, Idioma, Pantalla, Sala as SalaT, Subtitulo } from "../lib/sala";

type Escala = "normal" | "grande" | "enorme";

export default function Sala() {
  const [params] = useState(() => leerParametros(typeof location === "undefined" ? "" : location.search));
  const [sala, setSala] = useState<SalaT | null>(null);
  const [noExiste, setNoExiste] = useState(false);
  const [idioma, setIdioma] = useState<Idioma>(params.idioma ?? "es");
  const [crudos, setCrudos] = useState<Subtitulo[]>([]);
  const [estado, setEstado] = useState<EstadoSala | null>(null);
  const [conectado, setConectado] = useState(false);
  const [escala, setEscala] = useState<Escala>("grande");
  const [oscuro, setOscuro] = useState(false);
  const [ahora, setAhora] = useState(() => Date.now());
  const [nivel, setNivel] = useState(-90);
  const inter = useRef<InteraccionesApi | null>(null);
  const registrar = useCallback((api: InteraccionesApi) => { inter.current = api; }, []);

  useEffect(() => {
    if (!params.sala) return setNoExiste(true);
    fetch(`/api/salas/${params.sala}`)
      .then((r) => (r.status === 404 ? (setNoExiste(true), null) : r.json()))
      .then((s) => {
        if (!s) return;
        setSala(s);
        if (!params.idioma) setIdioma(s.idioma === "en" ? "es" : "en");
      })
      .catch(() => {});
    // Los eventos se guardan crudos (con los dos idiomas): cambiar de
    // idioma re-dibuja todo lo ya dicho, sin volver a pedirlo.
    return escuchar(params.sala, {
      subtitulo: (s) => setCrudos((c) => [...c.slice(-80), s]),
      estado: setEstado,
      conexion: setConectado,
      nivel: setNivel,
      reacciones: (r) => inter.current?.llegaron(r),
      preguntas: (p) => inter.current?.publicas(p),
    });
  }, [params.sala, params.idioma]);

  useEffect(() => {
    const t = setInterval(() => setAhora(Date.now()), 15_000);
    return () => clearInterval(t);
  }, []);

  const pantalla: Pantalla = useMemo(
    () => crudos.reduce((p, s) => aplicar(p, s, idioma), PANTALLA_VACIA),
    [crudos, idioma],
  );

  useEffect(() => {
    if (sala) document.title = `${sala.nombre} · conf`;
    const u = new URL(location.href);
    u.searchParams.set("l", idioma);
    history.replaceState(null, "", u);
  }, [sala, idioma]);

  if (noExiste) {
    return (
      <div className="lamina">
        <PliegoTitulo size="seccion" as="h1">sala no encontrada</PliegoTitulo>
        <p className="prosa"><a href="/">Volver a la lista de salas</a></p>
      </div>
    );
  }

  const ultima = pantalla.lineas.at(-1);
  const vieja = ultima && ahora - ultima.t_fin > 60_000 ? hace(ahora - ultima.t_fin) : null;
  const motor = estado?.motor;

  return (
    <div className="lamina" style={{ gap: "1.25rem" }}>
      <header style={{ display: "grid", gap: ".75rem" }}>
        <p className="micro" style={{ margin: 0 }}><a href="/">← salas</a></p>
        <PliegoTitulo size="seccion" as="h1" bang={false}>{(sala?.nombre ?? params.sala).toLowerCase()}</PliegoTitulo>
        <div className="fila">
          <PliegoEnVivo estado={!conectado ? "sinver" : estado?.en_vivo ? "vivo" : "quieto"} grabado={sala?.demo} />
          <PliegoOnda nivel={conectado && estado?.en_vivo ? nivel : -90} label={`Nivel de la voz en la sala`} />
          {sala && (
            <PliegoEtiqueta tone="linea" size="sm">
              {sala.idioma === idioma
                ? `${NOMBRE_IDIOMA[idioma]} · original`
                : `${NOMBRE_IDIOMA[sala.idioma]} → ${NOMBRE_IDIOMA[idioma]}`}
            </PliegoEtiqueta>
          )}
          {!conectado && <PliegoEstado estado="sinver">reconectando…</PliegoEstado>}
          {conectado && motor === "degradado" && <PliegoEstado estado="aviso">el motor se está reiniciando</PliegoEstado>}
          {conectado && motor === "caido" && <PliegoEstado estado="mal">el motor no responde</PliegoEstado>}
        </div>
      </header>

      <section aria-label="Opciones de lectura" className="fila">
        <span className="micro">idioma</span>
        {(["es", "en"] as Idioma[]).map((l) => (
          <PliegoBoton key={l} size="sm" variant={idioma === l ? "solid" : "outline"} aria-pressed={idioma === l}
            onClick={() => setIdioma(l)}>{NOMBRE_IDIOMA[l]}</PliegoBoton>
        ))}
        <span className="micro" style={{ marginLeft: ".75rem" }}>letra</span>
        {(["normal", "grande", "enorme"] as Escala[]).map((e) => (
          <PliegoBoton key={e} size="sm" variant={escala === e ? "solid" : "outline"} aria-pressed={escala === e}
            onClick={() => setEscala(e)}>{e}</PliegoBoton>
        ))}
        <PliegoBoton size="sm" variant={oscuro ? "solid" : "outline"} aria-pressed={oscuro}
          onClick={() => setOscuro((o) => !o)}>fondo oscuro</PliegoBoton>
      </section>

      {vieja && <p className="micro" style={{ margin: 0 }}>Lo último que se dijo fue {vieja}.</p>}
      <PliegoSubtitulos
        label={`Subtítulos en ${NOMBRE_IDIOMA[idioma]}`}
        idioma={idioma}
        lineas={pantalla.lineas}
        provisional={pantalla.provisional}
        maxLineas={escala === "enorme" ? 2 : 4}
        escala={escala}
        tono={oscuro ? "tinta" : "hoja"}
        vacio={sala?.demo
          ? "La sala está sonando: la primera frase aparece en unos segundos."
          : "Esperando la primera frase."}
      />

      <PliegoFilete label="la sala" />
      <p className="micro" style={{ margin: 0 }}>
        {estado
          ? `${estado.espectadores} mirando · ${estado.atendio ? `transcribe ${estado.atendio}` : `motor pedido: ${estado.backend}`}${estado.lat_p50_ms ? ` · ${segundos(estado.lat_p50_ms)} de proceso por frase` : ""}`
          : "…"}
        {sala?.demo && " · audio de Nerdearla 2025 en bucle, transcrito y traducido en vivo"}
      </p>
      {sala && <Interacciones sala={sala.id} registrar={registrar} />}

      {sala && (
        <nav aria-label="Llevarse los subtítulos" className="fila micro">
          <a href={`/obs/?s=${sala.id}&l=${idioma}`}>overlay para OBS / vMix</a>
          <span aria-hidden="true">·</span>
          <a href={`/cartel/?s=${sala.id}&l=${idioma}`}>cartel para el proyector</a>
          <span aria-hidden="true">·</span>
          {(["srt", "vtt", "txt"] as const).map((f) => (
            <a key={f} href={`/api/salas/${sala.id}/export?formato=${f}&idioma=${idioma}`} download>{f}</a>
          ))}
        </nav>
      )}
    </div>
  );
}
