import { useEffect, useMemo, useState } from "react";
import { PliegoCartel, PliegoEnVivo, PliegoQR, PliegoSubtitulos } from "@ahroi/foundation/pliego";
import { aplicar, escuchar, leerParametros, NOMBRE_IDIOMA, PANTALLA_VACIA } from "../lib/sala";
import type { EstadoSala, Sala, Subtitulo } from "../lib/sala";
import { matrizQR } from "../lib/qr";

// El cartel del proyector de un escenario: /cartel/?s=<sala>&l=es. Pantalla
// completa, se ve de lejos, y el QR lleva a la dirección corta de la sala.
export default function Cartel() {
  const [{ sala: id, idioma }] = useState(() => leerParametros(location.search));
  const l = idioma ?? "es";
  const [sala, setSala] = useState<Sala | null>(null);
  const [estado, setEstado] = useState<EstadoSala | null>(null);
  const [conectado, setConectado] = useState(false);
  const [crudos, setCrudos] = useState<Subtitulo[]>([]);

  useEffect(() => {
    if (!id) return;
    fetch(`/api/salas/${id}`).then((r) => (r.ok ? r.json() : null)).then(setSala).catch(() => {});
    return escuchar(id, {
      subtitulo: (s) => setCrudos((c) => [...c.slice(-10), s]),
      estado: setEstado,
      conexion: setConectado,
    });
  }, [id]);

  const p = useMemo(() => crudos.reduce((a, s) => aplicar(a, s, l), PANTALLA_VACIA), [crudos, l]);
  const corta = `${location.host}/s/${id}`;
  const qr = useMemo(() => matrizQR(`${location.protocol}//${corta}`), [corta]);
  if (!id) return <p className="prosa">Falta ?s=&lt;sala&gt; en la URL.</p>;
  const origen = sala ? NOMBRE_IDIOMA[sala.idioma] : "";
  return (
    <PliegoCartel
      sobre={sala?.demo ? "sala demo · audio grabado" : "subtítulos en vivo"}
      titulo={sala?.nombre ?? id}
      idiomas={sala ? (sala.idioma === l ? `${origen} · original` : `${origen} → ${NOMBRE_IDIOMA[l]}`) : undefined}
      estado={<PliegoEnVivo estado={!conectado ? "sinver" : estado?.en_vivo ? "vivo" : "quieto"} grabado={sala?.demo} />}
      url={corta}
      qr={<PliegoQR matriz={qr} label={`Código QR que abre ${corta}`} lado={Math.min(260, Math.round(innerHeight * 0.28))} />}
      subtitulo={p.lineas.length > 0 ? (
        <PliegoSubtitulos label={`Subtítulos en ${NOMBRE_IDIOMA[l]}`} idioma={l} escala="enorme" maxLineas={1} lineas={p.lineas} />
      ) : undefined}
    />
  );
}
