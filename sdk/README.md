# @wacalls/web

Carrega o áudio das chamadas do WaCalls **dentro do seu front-end**, sem o painel.

A API do WaCalls disca, encerra, monitora e entrega gravações — mas nunca transporta áudio. O
som só existe quando um navegador negocia WebRTC com o servidor. Este SDK é esse navegador,
dentro da sua aplicação: o operador fala pela sua interface, com a sua identidade visual, e
nunca vê o painel.

Referências: [`../INTEGRACAO.md`](../INTEGRACAO.md) para a API HTTP, [`../API.md`](../API.md)
para a operação do servidor.

---

## Instalação

O pacote não está publicado no npm. Use uma das duas formas:

```bash
# caminho local (o script "prepare" compila o dist/ na instalação)
npm install file:../WaCalls/sdk
```

Ou simplesmente **copie `src/` para o seu projeto** — são ~600 linhas sem nenhuma dependência
de runtime.

Para compilar manualmente dentro do repositório:

```bash
cd sdk && npm install && npm run build
```

---

## Uso em três passos

```ts
import { WaCallsClient } from "@wacalls/web";

// 1. Um cliente por operador
const wacalls = new WaCallsClient({
  baseUrl: "https://seu-host.easypanel.host",
  token: import.meta.env.VITE_WACALLS_TOKEN,
  clientId: "corretor-42",   // estável por pessoa
});

// 2. Discar e já colocar o microfone na chamada
const call = await wacalls.startAndAttach({
  sessionId,
  phone: "5511999999999",
  record: true,
  maxLatencyMs: 400,
});

// 3. Tocar o áudio que chega
audioEl.srcObject = call.remoteStream;

call.on("state", (s) => console.log(s));  // connecting → connected → ended
call.mute(true);
await call.end();
```

### Assumir uma chamada que o seu backend criou

Uma chamada disparada pela API nasce **sem dono**. Qualquer operador pode assumir; o primeiro
que anexar o áudio fica com ela, e os outros recebem `409`.

```ts
wacalls.events.connect();

wacalls.events.on("call-list", ({ calls }) => {
  const livres = calls.filter((c) => c.owner === null && c.status !== "ended");
  // ...mostre na sua interface
});

const call = await wacalls.pickUp({ sessionId, callId });
```

### Chamadas recebidas

```ts
wacalls.events.on("incoming", async ({ sessionId, id, peer }) => {
  if (!confirm(`Atender ${peer}?`)) return wacalls.reject(sessionId, id);
  const call = await wacalls.answer({ sessionId, callId: id });
});
```

### Sessões e QR code

O QR nunca volta na resposta HTTP: ele chega pelo stream de eventos. Conecte `events` antes.

```ts
wacalls.events.connect();
wacalls.events.on("session-qr", ({ sessionId, qr }) => renderQr(qr)); // string para uma lib de QR
wacalls.events.on("auth-state", ({ sessionId, paired }) => paired && fecharModal());

const sid = await wacalls.createSession("Comercial"); // já dispara o pareamento
await wacalls.pairSession(sid);    // novo QR (400 se já estiver pareada)
await wacalls.logoutSession(sid);  // desconecta, mantém a sessão
await wacalls.deleteSession(sid);  // apaga; gravações e histórico ficam no disco
```

### React

```tsx
import { useWaCallsClient, useCallState, useRemoteAudio } from "@wacalls/web/react";

function Discador({ sessionId }: { sessionId: string }) {
  const wacalls = useWaCallsClient({ baseUrl, token, clientId: user.id });
  const [call, setCall] = useState<WaCallsCall | null>(null);
  const state = useCallState(call);
  const audioRef = useRemoteAudio(call);

  return (
    <>
      <audio ref={audioRef} autoPlay />
      <button
        disabled={state !== "idle" && state !== "ended"}
        onClick={async () => setCall(await wacalls.startAndAttach({ sessionId, phone }))}
      >
        {state === "connected" ? "Em chamada" : "Ligar"}
      </button>
    </>
  );
}
```

---

## Configuração obrigatória no servidor

O SDK roda em outro domínio, e a política padrão do servidor é same-origin — o bloqueio é
server-side, antes do handler, então **tudo volta `403 origin not allowed`** até liberar:

```
WACALLS_ALLOW_ORIGIN=https://app.doseuproduto.com
```

Aceita lista separada por vírgula. Não use `*`: isso permitiria que qualquer site visitado pelo
operador dirigisse as chamadas dele.

Duas restrições que vêm junto:

- **HTTPS é obrigatório.** `getUserMedia` e `audioWorklet.addModule` só existem em secure
  context. `localhost` é a única exceção, útil para desenvolvimento.
- **O token fica visível no front-end.** É o mesmo modelo do painel hoje: um único
  `WACALLS_TOKEN` para todos os operadores, sem login individual. Se o seu produto precisa de
  auditoria por pessoa, isso é trabalho no servidor — o SDK não resolve.

---

## Opções

### `new WaCallsClient(options)`

| Opção | Padrão | Descrição |
|---|---|---|
| `baseUrl` | — | origem do servidor, sem barra final |
| `token` | — | `WACALLS_TOKEN`. Use o token de operador, não a API key |
| `clientId` | uuid em localStorage | identidade do operador; **precisa sobreviver a reloads**, senão ele perde a chamada em que está |
| `storageKey` | `wacalls.clientId` | chave do clientId gerado; troque se dois produtos dividem a origem |
| `fetchImpl` | `globalThis.fetch` | para testes ou política de retry própria |

### `call.attachAudio(options)`

| Opção | Padrão | Descrição |
|---|---|---|
| `micDeviceId` | microfone padrão | `deviceId` de `enumerateDevices()` |
| `maxLatencyMs` | `2000` | teto do buffer de reprodução. **300–500 é melhor para conversa** — o padrão existe só para igualar o comportamento do painel |
| `audioConstraints` | — | mesclado no `getUserMedia`; use para desligar `echoCancellation` ou `noiseSuppression` |

---

## O protocolo, para quem preferir reimplementar

Tudo cabe em uma tabela. O servidor não valida nada disso — áudio no formato errado vira
silêncio ou voz acelerada, sem erro em lugar nenhum.

| | Uplink (navegador → servidor) | Downlink |
|---|---|---|
| Codificação | PCM **Int16 Little-Endian, mono, 16 kHz** | idêntico |
| Tamanho | 320 amostras = 640 bytes, a cada 20 ms | 960 amostras = 1920 bytes, ~60 ms |
| Framing | nenhum — PCM puro, sem cabeçalho | idem |

- Data channel: label **`"pcm"`**, `{ ordered: true }`, `binaryType = "arraybuffer"`.
- Handshake: `POST /api/sessions/{sid}/calls/{callId}/webrtc` com `{"sdp_offer": "..."}` →
  `{"sdp_answer": "..."}`. **Postar o offer é o ato de reivindicar a chamada** — não existe
  endpoint de claim separado.
- ICE: vanilla, sem trickle. Espere o gathering terminar antes de postar. Não há STUN nem TURN
  dos dois lados: o único caminho é o navegador alcançar o IP público do servidor na faixa UDP
  publicada.
- Reamostragem: não existe resampler no código. Tudo depende de
  `new AudioContext({ sampleRate: 16000 })`, que faz o navegador converter o microfone de
  48 kHz na entrada do grafo.

---

## O que este SDK corrige em relação ao código do painel

Não é uma cópia: a camada de áudio do painel só funciona por estar hospedada no mesmo domínio
do servidor, e delega o fim da chamada ao stream de eventos.

| Painel | SDK |
|---|---|
| Worklets carregados de `/worklets/*.js` — 404 em qualquer outro domínio | embutidos no bundle, carregados por `Blob` URL |
| Lê `?token=` e **apaga da barra de endereço** com `history.replaceState` | token injetado por configuração, sem efeito colateral |
| Espera o ICE gathering **sem timeout** — se travar, a chamada nunca conecta nem falha | timeout de 2 s, segue com os candidatos que juntou |
| Ninguém observa `pc.connectionState` | `onStateChange` e teardown automático |
| `close()` só acontece via SSE `call-ended`; sem stream, **o microfone fica aberto** | ciclo de vida próprio, funciona com ou sem SSE |
| Fechar a aba encerra a chamada só pelo timeout de ICE, segundos depois | `pagehide` → `DELETE` com `keepalive` |
| Destino de áudio estéreo com o processor escrevendo um canal | `channelCount: 1` |
| `sampleRate: 16000` assumido sem verificação | valida e falha com mensagem clara |
| Buffer de 2 s que acumula latência | `maxLatencyMs` configurável |
| Sem mute, sem troca de microfone | `mute()`, `setMicDevice()`, `getStats()` |

---

## Exemplo executável

`example/index.html` é uma página mínima que faz uma ligação real.

```bash
npm run build
npx http-server . -p 5173     # ou qualquer servidor estático
# abra http://localhost:5173/example/
```

Adicione `http://localhost:5173` ao `WACALLS_ALLOW_ORIGIN` do servidor antes de testar.
