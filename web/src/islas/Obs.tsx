import { useEffect, useMemo, useState } from "react";
import { PliegoSubtitulos } from "@ahroi/foundation/pliego";
import { aplicar, escuchar, leerParametros, NOMBRE_IDIOMA, PANTALLA_VACIA } from "../lib/sala";
import type { Subtitulo } from "../lib/sala";

// El overlay para OBS/vMix (Browser Source): SÓLO los subtítulos, sobre
// fondo transparente, abajo. Es la integración "quemar los subtítulos en
// el stream" de las bases: OBS compone esta página encima del video.
//   /obs/?s=<sala>&l=es&lineas=2&fondo=transparente|tinta
export default function Obs() {
  const [q] = useState(() => new URLSearchParams(location.search));
  const { sala, idioma } = leerParametros(location.search);
  const l = idioma ?? "es";
  const lineas = Math.min(4, Math.max(1, Number(q.get("lineas")) || 2));
  const tono = q.get("fondo") === "tinta" ? "tinta" : "transparente";
  const [crudos, setCrudos] = useState<Subtitulo[]>([]);

  useEffect(() => {
    if (!sala) return;
    return escuchar(sala, {
      subtitulo: (s) => setCrudos((c) => [...c.slice(-20), s]),
      estado: () => {},
      conexion: () => {},
    });
  }, [sala]);

  const p = useMemo(() => crudos.reduce((acc, s) => aplicar(acc, s, l), PANTALLA_VACIA), [crudos, l]);
  if (!sala) return <p style={{ color: "#fff", background: "#000", padding: 8 }}>Falta ?s=&lt;sala&gt; en la URL.</p>;
  return (
    <div style={{ position: "fixed", inset: "auto 0 0 0", padding: "0 4vw 5vh" }}>
      <PliegoSubtitulos label={`Subtítulos en ${NOMBRE_IDIOMA[l]}`} idioma={l} lineas={p.lineas}
        provisional={p.provisional} maxLineas={lineas} escala="enorme" tono={tono} />
    </div>
  );
}
